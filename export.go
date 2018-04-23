package main

import (
	"fmt"
	"io/ioutil"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/PuerkitoBio/goquery"
	"github.com/cbroglie/mustache"
	"github.com/google/go-github/github"
	"github.com/gorilla/feeds"
	"github.com/gosimple/slug"

	gfm "github.com/shurcooL/github_flavored_markdown"
)

type Label struct {
	Name string
	Link string
}

type Issue struct {
	Number     int
	Title      string
	Link       string // slugified title
	Content    string
	Summary    string
	Labels     []Label
	GithubLink string
	Created    time.Time
}

func exportFeed(issues []Issue) error {
	now := time.Now()
	feed := &feeds.Feed{
		Title:       baseCfg.Site.Title,
		Link:        &feeds.Link{Href: fmt.Sprintf("https://%s.github.io/%s", baseCfg.Repository.Users[0], baseCfg.Repository.Name)},
		Description: baseCfg.Site.OneLineDesc,
		Author:      &feeds.Author{Name: baseCfg.Site.Author, Email: baseCfg.Site.Mail},
		Created:     now,
	}
	feed.Items = []*feeds.Item{}

	for _, issue := range issues {
		item := &feeds.Item{
			Title:       issue.Title,
			Link:        &feeds.Link{Href: fmt.Sprintf("https://%s.github.io/%s/%s", baseCfg.Repository.Users[0], baseCfg.Repository.Name, issue.Link)},
			Description: issue.Summary,
			Author:      &feeds.Author{Name: baseCfg.Site.Author, Email: baseCfg.Site.Mail},
			Created:     issue.Created,
		}
		feed.Items = append(feed.Items, item)
	}

	atom, err := feed.ToAtom()
	if err != nil {
		return err
	}

	return ioutil.WriteFile(filepath.Join(outDir, feedFile), []byte(atom), 0755)
}

func prepareIssues(issues []*github.Issue, baseCfg BaseConfig) ([]Issue, error) {
	export := []Issue{}

	for _, issue := range issues {
		exIssue := Issue{
			Title:      issue.GetTitle(),
			Link:       fmt.Sprintf("%d-%s.html", issue.GetNumber(), slug.Make(issue.GetTitle())),
			Created:    issue.GetCreatedAt(),
			Labels:     []Label{},
			GithubLink: issue.GetHTMLURL(),
			Number:     issue.GetNumber(),
		}

		// TODO maybe add syntax highlighting with chroma here?
		if exIssue.Title != "" {
			exIssue.Content = string(gfm.Markdown([]byte(issue.GetBody())))
			doc, err := goquery.NewDocumentFromReader(strings.NewReader(exIssue.Content))
			if err == nil {
				// Use first paragraph(p) as summary.
				firstp := doc.Find("p").First()
				html, err := firstp.Html()
				if err == nil {
					exIssue.Summary = html
				}
			}

			for _, label := range issue.Labels {
				l := Label{
					Name: label.GetName(),
					Link: "label-" + label.GetName() + ".html",
				}
				// If there is a label called "draft" we skip this issue completely.
				if l.Name == "draft" {
					goto SKIP_EXPORT
				}
				exIssue.Labels = append(exIssue.Labels, l)
			}

			export = append(export, exIssue)

		SKIP_EXPORT:
		}
		// We ignore issues with empty titles.
	}

	thisRepoURL := "https://github.com/" + baseCfg.Repository.Users[0] + "/" + baseCfg.Repository.Name + "/issues/"
	// Post processing loop over issues.
	for i := 0; i < len(export); i++ {
		doc, err := goquery.NewDocumentFromReader(strings.NewReader(export[i].Content))
		if err == nil {
			// Replace links that point to other intra-repo issues with intro-blog links.
			doc.Find("body a").Each(func(index int, item *goquery.Selection) {
				link, ok := item.Attr("href")
				if ok {
					issueNumStr := strings.TrimPrefix(link, thisRepoURL)
					if issueNumStr != link {
						// The link links to another issue. Let's extract the number
						issueNumStr = strings.Trim(issueNumStr, " /")
						issueNum, err := strconv.Atoi(issueNumStr)
						if err == nil {
							// Now that we have the issue number. Find the intra-blog link and replace the link.
							for _, iss := range export {
								if iss.Number == issueNum {
									item.SetAttr("href", iss.Link)
									break
								}
							}
						} // We just ignore errors and don't change the links in those cases.
					}
				}
			})

			// Replace the old HTML document with the edited one.
			nhtml, err := doc.Html()
			if err == nil {
				export[i].Content = nhtml
			}
		}
	}

	return export, nil
}

func BuildSite(issues []*github.Issue, baseCfg BaseConfig, themeCfg ThemeConfig) error {
	// Prepare issues for exporting.
	exIssues, err := prepareIssues(issues, baseCfg)
	if err != nil {
		return err
	}

	// Export issue single pages.
	for _, exis := range exIssues {
		err := exportIssue(exis, baseCfg, themeCfg)
		if err != nil {
			return err
		}
	}

	// Export Atom feed.
	err = exportFeed(exIssues)
	if err != nil {
		return err
	}

	// Export index page.
	err = exportTemplate(nil, themeCfg.IndexTemplate, exIssues, baseCfg, themeCfg)
	if err != nil {
		return err
	}

	// Export label index pages.
	err = exportLabelIndexes(themeCfg.IndexTemplate, exIssues, baseCfg, themeCfg)
	if err != nil {
		return err
	}

	// Export custom other templates
	for _, tmpl := range themeCfg.OtherTemplates {
		err = exportTemplate(nil, tmpl, exIssues, baseCfg, themeCfg)
		if err != nil {
			return err
		}
	}

	return nil
}

func exportLabelIndexes(template Template, issues []Issue, baseCfg BaseConfig, themeCfg ThemeConfig) error {
	labels := map[string]Label{}
	issuesByLabel := map[string][]Issue{}
	data := map[string]interface{}{}

	// Collect all labels and corresponding issues.
	for _, issue := range issues {
		for _, l := range issue.Labels {
			labels[l.Name] = l
			issuesByLabel[l.Name] = append(issuesByLabel[l.Name], issue)
		}
	}

	// Now export all issues by name.
	for l, lIssues := range issuesByLabel {
		data["SelectedLabel"] = labels[l]
		tmpl := Template{
			Source: template.Source,
			Layout: template.Layout,
			Target: labels[l].Link,
		}
		err := exportTemplate(data, tmpl, lIssues, baseCfg, themeCfg)
		if err != nil {
			return err
		}
	}

	return nil
}

func exportTemplate(data map[string]interface{}, template Template, issues []Issue, baseCfg BaseConfig, themeCfg ThemeConfig) error {
	if data == nil {
		data = map[string]interface{}{}
	}
	data["Site"] = baseCfg.Site
	data["Repository"] = baseCfg.Repository
	data["Today"] = time.Now()
	data["Issues"] = issues
	data["Custom"] = baseCfg.Custom
	data["Theme"] = themeCfg

	var tmpl string
	var err error
	if template.Layout == "" {
		tmpl, err = mustache.RenderFile(filepath.Join(themeDir, template.Source), data)
	} else {
		tmpl, err = mustache.RenderFileInLayout(filepath.Join(themeDir, template.Source), filepath.Join(themeDir, template.Layout), data)
	}
	if err != nil {
		return err
	}

	outname := filepath.Join(outDir, template.Target)
	return ioutil.WriteFile(outname, []byte(tmpl), 0755)
}

func exportIssue(issue Issue, baseCfg BaseConfig, themeCfg ThemeConfig) error {
	data := map[string]interface{}{
		"Site":       baseCfg.Site,
		"Repository": baseCfg.Repository,
		"Today":      time.Now(),
		"Issue":      issue,
		"Custom":     baseCfg.Custom,
		"Theme":      themeCfg,
	}
	issueTmpl, err := mustache.RenderFileInLayout(filepath.Join(themeDir, "issue.mustache"), filepath.Join(themeDir, "layout.mustache"), data)
	if err != nil {
		return err
	}
	outname := filepath.Join(outDir, issue.Link)
	return ioutil.WriteFile(outname, []byte(issueTmpl), 0755)
}
