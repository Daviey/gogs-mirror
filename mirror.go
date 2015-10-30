package main

import (
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"regexp"
	"sync"

	"github.com/cheggaaa/pb"
	"github.com/davecgh/go-spew/spew"
	gogsapi "github.com/gogits/go-gogs-client"
	githubapi "github.com/google/go-github/github"
	"golang.org/x/oauth2"
)

var (
	dryRun        bool
	mirror        bool
	includeForks  bool
	repoType      string
	excludeFilter []*regexp.Regexp
	includeFilter []*regexp.Regexp

	gogsURL     string
	gogsToken   string
	gogsUser    string
	githubToken string
	githubUser  string

	gogsUserID      int
	githubTokenUser string
)

func init() {
	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, "%s [options] [pattern ..]\n", os.Args[0])
		fmt.Fprintln(os.Stderr, "  pattern")
		fmt.Fprintln(os.Stderr, "    \tPCRE regexp that full repo names (user/repo) must match.")
		fmt.Fprintln(os.Stderr, "    \tPatterns prefixed with a dash (-) must not be matched.")
		flag.PrintDefaults()
	}

	flag.BoolVar(&dryRun, "dry-run", false, "Only print information about the migrations that would be performed.")
	flag.BoolVar(&mirror, "mirror", true, "Create the Gogs repositories as mirrors")
	flag.BoolVar(&includeForks, "include-forks", false, "Include forks")
	flag.StringVar(&repoType, "repo-type", "owner", "all | owner | public | private | member")

	flag.StringVar(&gogsURL, "gogs-url", "", "URL of the target Gogs instance")
	flag.StringVar(&gogsToken, "gogs-token", "", "Gogs API token")
	flag.StringVar(&gogsUser, "gogs-user", "", "Gogs target user")
	flag.StringVar(&githubToken, "github-token", "", "GitHub API token")
	flag.StringVar(&githubUser, "github-user", "", "GitHub source user")
}

var (
	gogs   *gogsapi.Client
	github *githubapi.Client
)

func main() {
	flag.Parse()
	if repoType == "" || gogsURL == "" || gogsToken == "" || gogsUser == "" || githubToken == "" {
		flag.Usage()
		os.Exit(2)
	}

	for _, filter := range flag.Args() {
		first := filter[0:1]
		if first == "-" {
			filter = filter[1:]
		} else {
			first = ""
		}

		re, err := regexp.Compile(filter)
		if err != nil {
			log.Fatalf("could not parse %s%s: %s", first, filter, err)
		}

		if first == "-" {
			excludeFilter = append(excludeFilter, re)
		} else {
			includeFilter = append(includeFilter, re)
		}
	}

	gogs = gogsapi.NewClient(gogsURL, gogsToken)
	var githubHttp *http.Client
	if githubToken != "" {
		githubHttp = oauth2.NewClient(oauth2.NoContext,
			oauth2.StaticTokenSource(&oauth2.Token{AccessToken: githubToken}))
	}
	github = githubapi.NewClient(githubHttp)

	githubUserData, _, err := github.Users.Get("")
	if err != nil {
		log.Fatalf("couldn't fetch GitHub user: %s", err)
	}
	githubTokenUser = *githubUserData.Login

	opts := &githubapi.RepositoryListOptions{
		Type: repoType,
		ListOptions: githubapi.ListOptions{
			Page:    0,
			PerPage: 100,
		},
	}

	var repos []githubapi.Repository

	for {
		pageRepos, resp, err := github.Repositories.List(githubUser, opts)
		if err != nil {
			log.Fatalf("couldn't fetch GitHub repository list: %s", err)
		}

	repoLoop:
		for _, repo := range pageRepos {
			if !includeForks && *repo.Fork {
				continue
			}

			if includeFilter != nil {
				for _, re := range includeFilter {
					if !re.Match([]byte(*repo.FullName)) {
						continue repoLoop
					}
				}
			}

			if excludeFilter != nil {
				for _, re := range excludeFilter {
					if re.Match([]byte(*repo.FullName)) {
						continue repoLoop
					}
				}
			}

			fmt.Println(*repo.FullName)
			repos = append(repos, repo)
		}

		opts.Page = resp.NextPage
		if resp.NextPage == 0 {
			break
		}
	}

	gogsUserData, err := gogs.GetUserInfo(gogsUser)
	if err != nil {
		log.Fatalf("couldn't fetch Gogs user: %s", err)
	}
	gogsUserID = int(gogsUserData.ID)

	log.Printf("preparing to copy %d repos", len(repos))
	var (
		bar *pb.ProgressBar
		wg  sync.WaitGroup
	)

	if !dryRun {
		bar = pb.StartNew(len(repos))
	}

	gogsRepos := make([]*gogsapi.Repository, len(repos))
	for i, repo := range repos {
		var repoDescription string
		if repo.Description != nil {
			repoDescription = *repo.Description
		}

		opts := gogsapi.MigrateRepoOption{
			CloneAddr:    *repo.CloneURL,
			AuthUsername: githubTokenUser,

			Private:     *repo.Private,
			UID:         gogsUserID,
			RepoName:    *repo.Name,
			Description: repoDescription,
			Mirror:      mirror,
		}

		if dryRun {
			spew.Dump(opts)
			continue
		}

		opts.AuthPassword = githubToken

		wg.Add(1)
		i := i
		go func() {
			defer wg.Done()
			defer bar.Increment()

			gogsRepo, err := gogs.MigrateRepo(opts)
			if err != nil {
				log.Printf("failed to migrate repo %s: %s", *repo.FullName, err)
				return
			}

			gogsRepos[i] = gogsRepo
		}()
	}
	wg.Wait()
}
