package main

import (
	"fmt"
	"html/template"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

type Post struct {
	Slug    string
	Title   string
	Date    time.Time
	Summary string
	Tags    []string
	Body    template.HTML
	Words   int
}

func (p Post) Stamp() string { return p.Date.Format("2 January 2006") }

func (p Post) ISO() string { return p.Date.Format("2006-01-02") }

func (p Post) ReadingTime() int {
	minutes := (p.Words + 219) / 220
	if minutes < 1 {
		return 1
	}
	return minutes
}

func loadPosts(dir string) ([]Post, error) {
	names, err := filepath.Glob(filepath.Join(dir, "*.md"))
	if err != nil {
		return nil, err
	}

	var posts []Post
	for _, name := range names {
		post, err := readPost(name)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", name, err)
		}
		posts = append(posts, post)
	}

	sort.Slice(posts, func(i, j int) bool {
		if posts[i].Date.Equal(posts[j].Date) {
			return posts[i].Slug < posts[j].Slug
		}
		return posts[i].Date.After(posts[j].Date)
	})
	return posts, nil
}

func readPost(path string) (Post, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Post{}, err
	}

	front, body, err := splitFrontMatter(string(data))
	if err != nil {
		return Post{}, err
	}

	post := Post{
		Slug:  strings.TrimSuffix(filepath.Base(path), ".md"),
		Words: len(strings.Fields(body)),
		Body:  template.HTML(renderMarkdown(body)),
	}

	for key, value := range front {
		switch key {
		case "title":
			post.Title = value
		case "summary":
			post.Summary = value
		case "slug":
			post.Slug = value
		case "tags":
			for _, tag := range strings.Split(value, ",") {
				if tag = strings.TrimSpace(tag); tag != "" {
					post.Tags = append(post.Tags, tag)
				}
			}
		case "date":
			post.Date, err = time.Parse("2006-01-02", value)
			if err != nil {
				return Post{}, fmt.Errorf("date %q is not YYYY-MM-DD", value)
			}
		}
	}

	if post.Title == "" {
		return Post{}, fmt.Errorf("no title in front matter")
	}
	if post.Date.IsZero() {
		return Post{}, fmt.Errorf("no date in front matter")
	}
	return post, nil
}

func splitFrontMatter(src string) (map[string]string, string, error) {
	src = strings.ReplaceAll(src, "\r\n", "\n")
	if !strings.HasPrefix(src, "---\n") {
		return nil, "", fmt.Errorf("missing --- front matter block")
	}
	end := strings.Index(src[4:], "\n---")
	if end < 0 {
		return nil, "", fmt.Errorf("front matter block is not closed")
	}

	front := map[string]string{}
	for _, line := range strings.Split(src[4:4+end], "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		key, value, found := strings.Cut(line, ":")
		if !found {
			return nil, "", fmt.Errorf("front matter line %q is not key: value", line)
		}
		front[strings.TrimSpace(key)] = strings.Trim(strings.TrimSpace(value), `"`)
	}

	body := src[4+end+4:]
	return front, strings.TrimLeft(body, "\n"), nil
}
