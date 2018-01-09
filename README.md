# Github Selector

A small tool that presents a list of repositories using the fzf fuzzy matcher and then clones the selected one

## Setup

1. `brew install fzf`
2. `brew install git`
3. `make`
4. Add a function to your bashrc or zshrc

```
gsa() {
    cd "$($HOME/go/src/github.com/ctompkinson/github-selector/github_selector)"
}
```