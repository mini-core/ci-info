package controller

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/ioutil"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/google/go-github/v32/github"
	"github.com/sirupsen/logrus"
	gh "github.com/suzuki-shunsuke/ci-info/pkg/github"
	"github.com/suzuki-shunsuke/go-ci-env/cienv"
)

type Params struct {
	Owner       string
	Repo        string
	SHA         string
	Dir         string
	PRNum       int
	GitHubToken string
	LogLevel    string
	Prefix      string
}

func (ctrl Controller) getPR(ctx context.Context, params Params) (*github.PullRequest, error) {
	prNum := params.PRNum
	if prNum <= 0 {
		logrus.WithFields(logrus.Fields{
			"owner": params.Owner,
			"repo":  params.Repo,
			"sha":   params.SHA,
		}).Debug("get pull request from SHA")
		prs, _, err := ctrl.GitHub.ListPRsWithCommit(ctx, gh.ParamsListPRsWithCommit{
			Owner: params.Owner,
			Repo:  params.Repo,
			SHA:   params.SHA,
		})
		if err != nil {
			return nil, err
		}
		logrus.WithFields(logrus.Fields{
			"size": len(prs),
		}).Debug("the number of pull requests assosicated with the commit")
		if len(prs) == 0 {
			return nil, nil
		}
		prNum = prs[0].GetNumber()
	}
	pr, _, err := ctrl.GitHub.GetPR(ctx, gh.ParamsGetPR{
		Owner: params.Owner,
		Repo:  params.Repo,
		PRNum: prNum,
	})
	if err != nil {
		return nil, err
	}
	return pr, nil
}

func New(ctx context.Context, params Params) (Controller, Params, error) {
	if params.LogLevel != "" {
		lvl, err := logrus.ParseLevel(params.LogLevel)
		if err != nil {
			logrus.WithFields(logrus.Fields{
				"log_level": params.LogLevel,
			}).WithError(err).Error("the log level is invalid")
		}
		logrus.SetLevel(lvl)
	}

	if params.GitHubToken == "" {
		params.GitHubToken = os.Getenv("GITHUB_TOKEN")
		if params.GitHubToken == "" {
			params.GitHubToken = os.Getenv("GITHUB_ACCESS_TOKEN")
		}
	}

	//nolint:nestif
	if platform := cienv.Get(); platform != nil {
		if params.Owner == "" {
			params.Owner = platform.RepoOwner()
		}
		if params.Repo == "" {
			params.Repo = platform.RepoName()
		}
		if params.SHA == "" {
			params.SHA = platform.SHA()
		}
		if params.PRNum <= 0 {
			prNum, err := platform.PRNumber()
			if err != nil {
				return Controller{}, params, err
			}
			params.PRNum = prNum
		}
	}

	return Controller{
		GitHub: gh.New(ctx, gh.ParamsNew{
			Token: params.GitHubToken,
		}),
		Stdout: os.Stdout,
		Stderr: os.Stderr,
	}, params, nil
}

var (
	errGitHubTokenRequired = errors.New("GitHub Access Token is required")
	errOwnerRequired       = errors.New("owner is required")
	errRepoRequired        = errors.New("repo is required")
	errSHAOrPRNumRequired  = errors.New("sha or pr number is required")
)

func (ctrl Controller) Run(ctx context.Context, params Params) error {
	if params.GitHubToken == "" {
		return errGitHubTokenRequired
	}
	if params.Owner == "" {
		return errOwnerRequired
	}
	if params.Repo == "" {
		return errRepoRequired
	}
	if params.PRNum <= 0 && params.SHA == "" {
		return errSHAOrPRNumRequired
	}
	pr, err := ctrl.getPR(ctx, params)
	if err != nil {
		return err
	}

	if pr == nil {
		return nil
	}

	files, _, err := ctrl.GitHub.GetPRFiles(ctx, gh.ParamsGetPRFiles{
		Owner:    params.Owner,
		Repo:     params.Repo,
		PRNum:    pr.GetNumber(),
		FileSize: pr.GetChangedFiles(),
	})
	if err != nil {
		return err
	}

	dir := params.Dir
	if dir == "" {
		d, err := ioutil.TempDir("", "ci-info")
		if err != nil {
			return err
		}
		dir = d
	}

	ctrl.printEnvs(params.Prefix, dir, pr)

	if err := ctrl.writePRFilesJSON(filepath.Join(dir, "pr_files.json"), files); err != nil {
		return err
	}

	if err := ctrl.writePRJSON(filepath.Join(dir, "pr.json"), pr); err != nil {
		return err
	}

	if err := ctrl.writePRFilesTxt(filepath.Join(dir, "pr_files.txt"), files); err != nil {
		return err
	}

	if err := ctrl.writeLabelsTxt(filepath.Join(dir, "labels.txt"), pr.Labels); err != nil {
		return err
	}
	return nil
}

func (ctrl Controller) writeLabelsTxt(p string, labels []*github.Label) error {
	labelNames := make([]string, len(labels))
	for i, label := range labels {
		labelNames[i] = label.GetName()
	}
	txt := ""
	if len(labelNames) != 0 {
		txt = strings.Join(labelNames, "\n") + "\n"
	}
	//nolint:gosec
	if err := ioutil.WriteFile(p, []byte(txt), 0x755); err != nil {
		return err
	}
	return nil
}

func (ctrl Controller) writePRFilesTxt(p string, files []*github.CommitFile) error {
	prFileNames := make([]string, len(files))
	for i, file := range files {
		prFileNames[i] = file.GetFilename()
	}
	txt := ""
	if len(prFileNames) != 0 {
		txt = strings.Join(prFileNames, "\n") + "\n"
	}
	//nolint:gosec
	if err := ioutil.WriteFile(p, []byte(txt), 0x755); err != nil {
		return err
	}
	return nil
}

func (ctrl Controller) writePRJSON(p string, pr *github.PullRequest) error {
	prJSON, err := os.Create(p)
	if err != nil {
		return err
	}
	defer prJSON.Close()
	if err := json.NewEncoder(prJSON).Encode(pr); err != nil {
		return err
	}
	return nil
}

func (ctrl Controller) writePRFilesJSON(p string, files []*github.CommitFile) error {
	prFilesJSON, err := os.Create(p)
	if err != nil {
		return err
	}
	defer prFilesJSON.Close()
	if err := json.NewEncoder(prFilesJSON).Encode(files); err != nil {
		return err
	}
	return nil
}

func (ctrl Controller) printEnvs(prefix, dir string, pr *github.PullRequest) {
	fmt.Fprintln(ctrl.Stdout, strings.Join([]string{
		"export " + prefix + "PR_NUMBER=" + strconv.Itoa(pr.GetNumber()),
		"export " + prefix + "BASE_REF=" + pr.GetBase().GetRef(),
		"export " + prefix + "HEAD_REF=" + pr.GetHead().GetRef(),
		"export " + prefix + "PR_AUTHOR=" + pr.GetUser().GetLogin(),
		"export " + prefix + "TEMP_DIR=" + dir,
	}, "\n"))
}
