package main

import (
	"context"
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

	workaround1862 bool

	gogsURL     string
	gogsToken   string
	gogsUser    string
	gogsOrg     string
	githubToken string
	githubUser  string
)

func init() {
	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, "%s [options] [pattern ..]\n", os.Args[0])
		fmt.Fprintln(os.Stderr, "  pattern")
		fmt.Fprintln(os.Stderr, "    \tPCRE regexp that full repo names (user/repo) must match.")
		fmt.Fprintln(os.Stderr, "    \tPatterns prefixed with a dash (-) must not be matched.")
		flag.PrintDefaults()
	}

	flag.BoolVar(&workaround1862, "workaround-1862", false, `Swap the "private" and "mirror" Gogs API fields (workaround for https://github.com/gogits/gogs/pull/1862)`)

	flag.BoolVar(&dryRun, "dry-run", false, "Only print information about the migrations that would be performed.")
	flag.BoolVar(&mirror, "mirror", true, "Create the Gogs repositories as mirrors")
	flag.BoolVar(&includeForks, "include-forks", false, "Include forks")
	flag.StringVar(&repoType, "repo-type", "owner", "all | owner | public | private | member")

	flag.StringVar(&gogsURL, "gogs-url", "", "URL of the target Gogs instance")
	flag.StringVar(&gogsToken, "gogs-token", "", "Gogs API token")
	flag.StringVar(&gogsUser, "gogs-user", "", "Gogs target user")
	flag.StringVar(&gogsOrg, "gogs-organization", "", "(Optional) Target organization to push to, if not set push to user account")
	flag.StringVar(&githubToken, "github-token", "", "GitHub API token")
	flag.StringVar(&githubUser, "github-user", "", "GitHub source user")
}

var (
	gogs             *gogsapi.Client
	gogsOrganization *gogsapi.Organization
	github           *githubapi.Client
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
	ctx := context.Background()

	githubTokenUserData, _, err := github.Users.Get(ctx, "")
	if err != nil {
		log.Fatalf("couldn't fetch GitHub user: %s", err)
	}
	githubTokenUser := *githubTokenUserData.Login

	githubUserData, _, err := github.Users.Get(ctx, githubUser)
	if err != nil {
		log.Fatalf("couldn't fetch GitHub user: %s", err)
	}
	githubUserIsOrg := githubUserData.Type != nil && *githubUserData.Type == "Organization"

	listOpts := githubapi.ListOptions{
		Page:    0,
		PerPage: 100,
	}

	var repos []githubapi.Repository

	for {
		var (
			pageRepos []*githubapi.Repository
			resp      *githubapi.Response
			err       error
		)
		if githubUserIsOrg {
			pageRepos, resp, err = github.Repositories.ListByOrg(ctx, githubUser, &githubapi.RepositoryListByOrgOptions{
				Type:        repoType,
				ListOptions: listOpts,
			})
		} else {
			pageRepos, resp, err = github.Repositories.List(ctx, githubUser, &githubapi.RepositoryListOptions{
				Type:        repoType,
				ListOptions: listOpts,
			})
		}
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
			repos = append(repos, *repo)
		}

		listOpts.Page = resp.NextPage
		if resp.NextPage == 0 {
			break
		}
	}

	var gogsOwnerID int
	var gogsOwnerName string
	if gogsOrg == "" {
		gogsUserData, err := gogs.GetUserInfo(gogsUser)
		if err != nil {
			log.Fatalf("couldn't fetch Gogs user: %s", err)
		}
		gogsOwnerID = int(gogsUserData.ID)
		gogsOwnerName = gogsUser

	} else {
		gogsOrganization, err := gogs.GetOrg(gogsOrg)
		if err != nil {
			log.Fatalf("couldn't fetch Gogs Organization: %s", err)
		}
		gogsOwnerID = int(gogsOrganization.ID)
		gogsOwnerName = gogsOrg

	}

	log.Printf("preparing to copy %d repos", len(repos))
	var (
		bar *pb.ProgressBar
		wg  sync.WaitGroup
	)

	if !dryRun {
		bar = pb.StartNew(len(repos))
	}

	concurrency := 10
	sem := make(chan bool, concurrency)
	gogsRepos := make([]*gogsapi.Repository, len(repos))
	for i, repo := range repos {

		var repoDescription string
		if repo.Description != nil {
			repoDescription = *repo.Description

			if len(repoDescription) > 255 {
				repoDescription = repoDescription[:255]
			}

		}

		opts := gogsapi.MigrateRepoOption{
			CloneAddr:    *repo.CloneURL,
			AuthUsername: githubTokenUser,

			Private:     *repo.Private,
			UID:         gogsOwnerID,
			RepoName:    *repo.Name,
			Description: repoDescription,
			Mirror:      mirror,
		}

		if dryRun {
			spew.Dump(opts)
			continue
		}

		opts.AuthPassword = githubToken
		if workaround1862 {
			opts.Mirror, opts.Private = opts.Private, opts.Mirror
		}

		wg.Add(1)
		sem <- true
		i := i
		go func(reponame string, reposhortname string, v int) {
			defer func() { <-sem }()
			defer wg.Done()
			defer bar.Increment()

			_, err := gogs.GetRepo(gogsOwnerName, reposhortname)

			// check to see if repo already there
			if err == nil {
				log.Printf("skipping already present repo: %s -> %s/%s:", reponame, gogsOwnerName, reposhortname)
				return
			}

			gogsRepo, err := gogs.MigrateRepo(opts)
			if err != nil {
				log.Printf("failed to migrate repo %s: %s", reponame, err)
				return
			}

			gogsRepos[v] = gogsRepo
		}(*repo.FullName, *repo.Name, i)
	}

	for i := 0; i < cap(sem); i++ {
		sem <- true
	}

	wg.Wait()
	if bar != nil {
		bar.Update()
	}
}
