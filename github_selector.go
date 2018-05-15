package main

import (
	"bufio"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"io/ioutil"
	"os"
	"os/exec"
	"os/user"
	"strings"

	"github.com/google/go-github/github"
	"github.com/pkg/errors"
	"golang.org/x/oauth2"
	"gopkg.in/mattes/go-expand-tilde.v1"
	"gopkg.in/src-d/go-git.v4"
	"log"
)

type GithubSelector struct {
	GithubToken string
	CloneDir    string
	OrgName     string
}

const configDir = ".config/github_selector"
const cacheFile = "cache.json"
const configFile = "config.json"

var homeDir string

func main() {
	refresh := flag.Bool("refresh",false, "will refresh the list of github packages")
	function := flag.Bool("function", false, "print a bash function used to setup")
	flag.Parse()

	if *function {
		fmt.Println("gs() { cd $($GOPATH/bin/github_selector) }")
		os.Exit(0)
	}

	g := GithubSelector{}
	g.Run(*refresh)
}

func (g *GithubSelector) Run(refresh bool) {
	// Just initialize the config, this asks the user for config input
	// or reads from disk
	g.createOrLoadConfig()

	// If we refresh, load github repositories
	// else read the local cache and use that as the repository list
	var repos []github.Repository

	_, err := os.Stat(buildCachePath())
	if os.IsNotExist(err) || refresh {
		ptrRepos := g.listOrgRepos(g.OrgName, g.GithubToken)
		for _, r := range ptrRepos {
			repos = append(repos, *r)
		}
		g.writeRepoCache(repos)
	} else {
		repos = g.readRepoCache()
	}

	// spawn fzf and then allow the user to pick which repository they want
	filtered := g.withFilter("fzf -m", func(in io.WriteCloser) {
		for _, repo := range repos {
			fmt.Fprintln(in, *repo.FullName)
		}
	})

	// take the output from fzf and then use that to pick the repository from our repo list
	var selectedRepo github.Repository
	for _, repo := range repos {
		if *repo.FullName == filtered[0] {
			selectedRepo = repo
		}
	}

	// clone the repository
	g.cloneRepo(selectedRepo)

	// write out the repository name so that it can be used to cd by an external function
	fmt.Printf("%s/%s\n", g.CloneDir, *selectedRepo.Name)
}

func (g *GithubSelector) cloneRepo(githubRepo github.Repository) {
	basePath := g.CloneDir
	repoPath := basePath + "/" + *githubRepo.Name

	repo, err := git.PlainClone(repoPath, false, &git.CloneOptions{
		URL:      fmt.Sprintf("git@github.com:%s.git", *githubRepo.FullName),
		Progress: os.Stderr,
	})


	if err == git.ErrRepositoryAlreadyExists {
		return
	}

	if err != nil {
		log.Fatal(err)
	}


	config, err := repo.Config()
	if err != nil {
		log.Fatal(err)
	}

	defaultBranch := githubRepo.GetDefaultBranch()
	config.Branches[defaultBranch].Remote = "origin"
}

func (g *GithubSelector) withFilter(command string, input func(in io.WriteCloser)) []string {
	shell := os.Getenv("SHELL")
	if len(shell) == 0 {
		shell = "sh"
	}
	cmd := exec.Command(shell, "-c", command)
	cmd.Stderr = os.Stderr
	in, _ := cmd.StdinPipe()
	go func() {
		input(in)
		in.Close()
	}()
	result, _ := cmd.Output()
	return strings.Split(string(result), "\n")
}

func (g *GithubSelector) listOrgRepos(organizationName string, githubToken string) []*github.Repository {
	ctx := context.Background()
	ts := oauth2.StaticTokenSource(
		&oauth2.Token{AccessToken: githubToken},
	)
	tc := oauth2.NewClient(ctx, ts)

	client := github.NewClient(tc)
	opt := &github.RepositoryListByOrgOptions{
		ListOptions: github.ListOptions{PerPage: 1000},
	}

	// get all pages of results
	var allRepos []*github.Repository
	for {
		repos, resp, err := client.Repositories.ListByOrg(ctx, organizationName, opt)
		if err != nil {
			fmt.Println(err)
		}
		allRepos = append(allRepos, repos...)
		if resp.NextPage == 0 {
			break
		}
		opt.Page = resp.NextPage
	}

	return allRepos
}

/*
 * Config
 */

func (g *GithubSelector) createOrLoadConfig() error {
	configPath := buildConfigPath()

	// Ensure directory exists
	if _, err := os.Stat(buildConfigDirPath()); os.IsNotExist(err) {
		os.MkdirAll(buildConfigDirPath(), 0755)
	}

	// Ensure config exists and if not write the defaults and return them
	if _, err := os.Stat(buildConfigPath()); os.IsNotExist(err) {
		fmt.Fprintln(os.Stderr, "no config found, creating new config...")

		g.promptUserForConfig()
		data, err := json.Marshal(g)
		if err != nil {
			return errors.Wrap(err, "unable to create config")
		}

		fmt.Println(configPath)
		fmt.Println(string(data))
		err = ioutil.WriteFile(configPath, data, 0644)
		if err != nil {
			return errors.Wrap(err, "unable to write default config")
		}

		return nil
	}

	return g.loadConfig(buildConfigPath())
}

func (g *GithubSelector) promptUserForConfig() {
	var err error

	g.GithubToken, err = readString("Whats your github access token?")

	rawCloneDir, err := readString("Whats your git clone directory?")
	g.CloneDir, err = tilde.Expand(rawCloneDir)

	g.OrgName, err = readString("What organization do you want to search?")

	if err != nil {
		panic("Failed to parse config")
	}
}

func (g *GithubSelector) loadConfig(path string) error {
	b, err := ioutil.ReadFile(path)
	if err != nil {
		return errors.Wrap(err, "unable to load config")
	}

	var config GithubSelector
	err = json.Unmarshal(b, &config)
	if err != nil {
		return errors.Wrap(err, "unable to unmarshal config")
	}
	*g = config

	return nil
}

/*
 * Cache Management
 */

func (g *GithubSelector) writeRepoCache(repos []github.Repository) error {
	jsonRepos, err := json.Marshal(repos)
	if err != nil {
		return errors.Wrap(err, "unable to marshall repo JSON")
	}

	err = ioutil.WriteFile(buildCachePath(), jsonRepos, 0644)
	if err != nil {
		return errors.Wrap(err, "Unable to write cache to disk")
	}

	return nil
}

func (g *GithubSelector) readRepoCache() ([]github.Repository) {
	jsonRepos, err := ioutil.ReadFile(buildCachePath())
	if err != nil {
		panic(errors.Wrap(err, "Unable to read cache"))
	}

	var repos []github.Repository
	err = json.Unmarshal(jsonRepos, &repos)
	if err != nil {
		panic(errors.Wrap(err, "Unable to read cache"))
	}

	return repos
}

/*
 * Helpers
 */

func readString(message string) (string, error) {
	reader := bufio.NewReader(os.Stdin)
	fmt.Fprintf(os.Stderr, "%s : ", message)
	in, err := reader.ReadString('\n')
	return strings.TrimSpace(in), err
}

func getHomeDir() string {
	if homeDir != "" {
		return homeDir
	}

	usr, err := user.Current()
	if err != nil {
		panic(err)
	}

	return usr.HomeDir
}

func buildConfigDirPath() string {
	return fmt.Sprintf("%s/%s", getHomeDir(), configDir)
}

func buildConfigPath() string {
	return fmt.Sprintf("%s/%s/%s", getHomeDir(), configDir, configFile)
}

func buildCachePath() string {
	return fmt.Sprintf("%s/%s/%s", getHomeDir(), configDir, cacheFile)
}

