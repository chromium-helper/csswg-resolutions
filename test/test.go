package main

import (
	"github.com/chromium-helper/csswg-resolutions/monorail"
	"google.golang.org/api/idtoken"
	"context"
	//"fmt"
)

func main() {
	audience, err := monorail.GetAudience("prod")
	if err != nil {
		panic(err)
	}

  ctx := context.Background()
  token_source, err := idtoken.NewTokenSource(ctx, audience)
  if err != nil {
    panic(err)
  }

  service, err := monorail.NewIssuesService(ctx, "prod", token_source)
  if err != nil {
    panic(err)
  }

	//request := &monorail.ModifyIssueRequest{
	//	Project:     "chromium",
	//	Crbug: 
	//	//Comment: ""
	//	//Owner:       ""
	//	//CcList:      []string{""},
	//	Components: []string{""},
	//}

	//err = service.ModifyIssue(request)
	//if err != nil {
	//	panic(err)
	//}
	//fmt.Printf("Issue %d", issue.Id)
}

