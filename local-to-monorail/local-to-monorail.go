package main

import (
  "github.com/chromium-helper/csswg-resolutions/monorail"
  "golang.org/x/oauth2/google"
  "context"
)

const (
  kTarget = "prod"
)

func ComputeTokenSource(scope string) oauth2.TokenSource {
	return oauth2.ReuseTokenSource(nil, computeSource{account: "", scopes: []string{scope}})
}

type computeSource struct {
	account string
	scopes  []string
}

func main() {
  ctx := context.Background()
  //audience, err := monorail.GetAudience(kTarget)
  //if err != nil {
  //  panic(err)
  //}

  token_source, err := google.DefaultTokenSource(ctx/*, audience*/)
  if err != nil {
    panic(err)
  }

  service, err := monorail.NewIssuesService(ctx, kTarget, token_source)
  if err != nil {
    panic(err)
  }

  request := &monorail.CreateIssueRequest{
    Project: "chromium",
    Summary: "vmpstr test bug #2",
    Description: `hello, this is another test bug!\nthanks!\n`,
    Components: []string{},
  }
  err = service.CreateIssue(request)
  if err != nil {
    panic(err)
  }
}
