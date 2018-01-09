package main

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"github.com/google/go-github/github"
	"github.com/pkg/errors"
	"golang.org/x/oauth2"
	"gopkg.in/mattes/go-expand-tilde.v1"
	"gopkg.in/yaml.v2"
	"io"
	"io/ioutil"
	"log"
	"os"
	"os/exec"
	"os/user"
	"strings"
)

type Config struct {
	GithubToken string
	CloneDir    string
	OrgName     string
}

func withFilter(command string, input func(in io.WriteCloser)) []string {
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

func listOrgRepos(organizationName string, githubToken string) []*github.Repository {
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

func createOrLoadConfig() (Config, error) {
	usr, err := user.Current()
	if err != nil {
		log.Fatal(err)
	}

	configDir := fmt.Sprintf("%s/.config/github_selector", usr.HomeDir)
	configFile := fmt.Sprintf("%s/config.yaml", configDir)

	// Ensure directory exists
	if _, err := os.Stat(configDir); os.IsNotExist(err) {
		os.MkdirAll(configDir, 0755)
	}

	// Ensure config exists and if not write the defaults and return them
	if _, err := os.Stat(configFile); os.IsNotExist(err) {
		config := promptUserForConfig()
		b, err := yaml.Marshal(config)
		if err != nil {
			return Config{}, errors.Wrap(err, "unable to create default config")
		}

		err = ioutil.WriteFile(configFile, b, 0644)
		if err != nil {
			return Config{}, errors.Wrap(err, "unable to write default config")
		}

		return config, nil
	}

	return loadConfig(configFile)
}

func promptUserForConfig() Config {
	reader := bufio.NewReader(os.Stdin)
	fmt.Print("Whats your github access token? : ")
	githubToken, _ := reader.ReadString('\n')

	reader = bufio.NewReader(os.Stdin)
	fmt.Print("Whats your git clone directory? : ")
	rawCloneDir, _ := reader.ReadString('\n')
	cloneDir, _ := tilde.Expand(rawCloneDir)

	reader = bufio.NewReader(os.Stdin)
	fmt.Print("What organization do you want to search? : ")
	orgName, _ := reader.ReadString('\n')

	return Config{
		GithubToken: strings.TrimSpace(githubToken),
		CloneDir:    strings.TrimSpace(cloneDir),
		OrgName:     strings.TrimSpace(orgName),
	}
}

func loadConfig(path string) (Config, error) {
	b, err := ioutil.ReadFile(path)
	if err != nil {
		return Config{}, errors.Wrap(err, "unable to load config")
	}

	var config Config
	err = yaml.Unmarshal(b, &config)
	if err != nil {
		return Config{}, errors.Wrap(err, "unable to unmarshal config")
	}

	return config, nil
}

func main() {
	config, _ := createOrLoadConfig()
	repos := listOrgRepos(config.OrgName, config.GithubToken)

	filtered := withFilter("fzf -m", func(in io.WriteCloser) {
		for _, repo := range repos {
			fmt.Fprintln(in, *repo.FullName)
		}
	})

	var selectedRepo *github.Repository
	for _, repo := range repos {
		if *repo.FullName == filtered[0] {
			selectedRepo = repo
		}
	}

	basePath := config.CloneDir
	repoPath := basePath + "/" + *selectedRepo.Name

	args := []string{"clone", "git@github.com:" + *selectedRepo.FullName + ".git", repoPath}
	var stdoutBuf, stderrBuf bytes.Buffer
	cmd := exec.Command("git", args...)

	stdoutIn, _ := cmd.StdoutPipe()
	stderrIn, _ := cmd.StderrPipe()

	var errStdout, errStderr error
	stdout := io.MultiWriter(os.Stderr, &stdoutBuf)
	stderr := io.MultiWriter(os.Stderr, &stderrBuf)
	err := cmd.Start()
	if err != nil {
		log.Fatalf("cmd.Start() failed with '%s'\n", err)
	}

	go func() {
		_, errStdout = io.Copy(stdout, stdoutIn)
	}()

	go func() {
		_, errStderr = io.Copy(stderr, stderrIn)
	}()

	err = cmd.Wait()
	if err != nil {
		log.Fatalf("git clone failed with %s\n", err)
	}

	fmt.Println(repoPath)
}
