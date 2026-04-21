package gocli

import (
	"io"
	"os"
	"sync"
)

var (
	githubIOMu     sync.RWMutex
	githubIOStdout io.Writer
	githubIOStderr io.Writer
)

func currentGithubStdout() io.Writer {
	githubIOMu.RLock()
	value := githubIOStdout
	githubIOMu.RUnlock()
	if value == nil {
		return os.Stdout
	}
	return value
}

func currentGithubStderr() io.Writer {
	githubIOMu.RLock()
	value := githubIOStderr
	githubIOMu.RUnlock()
	if value == nil {
		return os.Stderr
	}
	return value
}

func withGithubIO(stdout io.Writer, stderr io.Writer, fn func() error) error {
	githubIOMu.Lock()
	prevStdout := githubIOStdout
	prevStderr := githubIOStderr
	if stdout != nil {
		githubIOStdout = stdout
	}
	if stderr != nil {
		githubIOStderr = stderr
	}
	githubIOMu.Unlock()
	defer func() {
		githubIOMu.Lock()
		githubIOStdout = prevStdout
		githubIOStderr = prevStderr
		githubIOMu.Unlock()
	}()
	return fn()
}

func githubIssueWithIO(cwd string, args []string, stdout io.Writer, stderr io.Writer) error {
	return withGithubIO(stdout, stderr, func() error {
		return withWorkIO(stdout, stderr, func() error {
			_, err := GithubIssue(cwd, args)
			return err
		})
	})
}

func githubReviewWithIO(cwd string, args []string, stdout io.Writer, stderr io.Writer) error {
	return withGithubIO(stdout, stderr, func() error {
		_, err := GithubReview(cwd, args)
		return err
	})
}

func githubReviewRulesWithIO(cwd string, args []string, stdout io.Writer, stderr io.Writer) error {
	return withGithubIO(stdout, stderr, func() error {
		return GithubReviewRules(cwd, args)
	})
}
