# gogs-mirror

  Migrate repositories from GitHub en masse.

    env GO15VENDOREXPERIMENT=1 go get code.nathan7.eu/nathan7/gogs-mirror

## Usage

    gogs-mirror [options] [pattern ..]
      pattern
          PCRE regexp that full repo names (user/repo) must match.
          Patterns prefixed with a dash (-) must not be matched.
      -dry-run
          Only print information about the migrations that would be performed.
      -github-token string
          GitHub API token
      -github-user string
          GitHub source user
      -gogs-token string
          Gogs API token
      -gogs-url string
          URL of the target Gogs instance
      -gogs-user string
          Gogs target user
      -include-forks
          Include forks
      -mirror
          Create the Gogs repositories as mirrors (default true)
      -repo-type string
          all | owner | public | private | member (default "owner")
      -workaround-1862
          Swap the "private" and "mirror" Gogs API fields (workaround for https://github.com/gogits/gogs/pull/1862)
