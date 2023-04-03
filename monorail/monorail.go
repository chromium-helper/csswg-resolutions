package monorail

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"golang.org/x/oauth2"
	"io/ioutil"
	"net/http"
	"strconv"
	"strings"
	"time"
)

type IssuesService struct {
	Token      *oauth2.Token
	HttpClient *http.Client
	ApiBase    string
}

type monorailIssue struct {
	Name  string `json:"name"`
	State struct {
		Status string `json:"status"`
	} `json:"status"`
	FieldValues []struct {
		Field string `json:"field"`
		Value string `json:"value"`
	} `json:"fieldValues"`
	Owner struct {
		User string `json:"user"`
	} `json:"owner"`
	CreatedTime  time.Time `json:"createTime"`
	ModifiedTime time.Time `json:"modifyTime"`
	ClosedTime   time.Time `json:"closeTime"`
	Title        string    `json:"summary"`
}

type Issue struct {
	Id int
	// TODO: Populate more stuff I guess?
}

func contains(needle string, haystack []string) bool {
	for _, candidate := range haystack {
		if needle == candidate {
			return true
		}
	}
	return false
}

func createHttpClient(token_source oauth2.TokenSource) (*http.Client, error) {
	transport := &oauth2.Transport{
		Source: token_source,
		Base:   http.DefaultTransport,
	}
	return &http.Client{
		Transport: transport,
		Timeout:   1 * time.Minute,
	}, nil
}

func GetAudience(target string) (string, error) {
	if !contains(target, []string{"prod", "dev", "staging"}) {
		return "", fmt.Errorf("target must be one of prod, dev, staging\n")
	}
	return fmt.Sprintf("https://monorail-%s.appspot.com", target), nil
}

func NewIssuesService(ctx context.Context, target string, token_source oauth2.TokenSource) (
	*IssuesService, error) {
	if !contains(target, []string{"prod", "dev", "staging"}) {
		return nil, fmt.Errorf("target must be one of prod, dev, staging\n")
	}

	api_base := fmt.Sprintf("https://api-dot-monorail-%s.appspot.com/prpc", target)

	token, err := token_source.Token()
	if err != nil {
		return nil, fmt.Errorf("token_source.Token: %v", err)
	}

	http_client, err := createHttpClient(token_source)
	if err != nil {
		return nil, fmt.Errorf("createHttpClient: %v", err)
	}

	return &IssuesService{
		Token:      token,
		HttpClient: http_client,
		ApiBase:    api_base,
	}, nil
}

func (s *IssuesService) invokeApi(payload []byte, service string, method string) ([]byte, error) {
	url := fmt.Sprintf("%s/monorail.v3.%s/%s", s.ApiBase, service, method)

	request, err := http.NewRequest("POST", url, bytes.NewBuffer(payload))
	if err != nil {
		return nil, fmt.Errorf("http.NewRequest: %v", err)
	}

	request.Header.Add("Authorization", fmt.Sprintf("Bearer %s\n", s.Token.AccessToken))
	request.Header.Add("Content-Type", "application/json")
	request.Header.Add("Accept", "application/json")

	response, err := s.HttpClient.Do(request)
	if err != nil {
		return nil, fmt.Errorf("http.Client.Do: %v", err)
	}
	defer response.Body.Close()

	result, err := ioutil.ReadAll(response.Body)
	if err != nil {
		return nil, fmt.Errorf("ioutil.ReadAll: %v", err)
	}

	if response.StatusCode != 200 {
		return nil, fmt.Errorf("http response %d (%s)\nbody: %s", response.StatusCode, http.StatusText(response.StatusCode), string(result))
	}

	return result[4:], nil
}

type ModifyIssueRequest struct {
	// "chromium"
	Project string

	Crbug				int
	Comment		  string
	Owner				string
	CcList			[]string

	Components []string
}

func (s *IssuesService) ModifyIssue(request *ModifyIssueRequest) error {
	type WireStatusType struct {
		Status string `json:"status"`
	}

	type WireUserType struct {
		User string `json:"user"`
	}

	type WireComponentType struct {
		Component string `json:"component"`
	}

	type WireIssueType struct {
		Name string `json:"name"`
		Owner *WireUserType `json:"owner,omitempty"`
		CcUsers			[]*WireUserType						`json:"cc_users,omitempty"`
		Status *WireStatusType `json:"status,omitempty"`
		Components  []*WireComponentType  `json:"components"`
	}

	type WireDeltaType struct {
		Issue *WireIssueType `json:"issue"`
		UpdateMask string `json:"update_mask"`
	}

	type WireRequestType struct {
		Deltas []*WireDeltaType `json:"deltas"`
		CommentContent string `json:"comment_content"`
		NotifyType string `json:"notify_type"`
	}

	var update_mask []string

	status := "Untriaged"
	var owner *WireUserType
	if request.Owner != "" {
		owner = &WireUserType{ User: fmt.Sprintf("users/%s", request.Owner) }
		status = "Assigned"
		update_mask = append(update_mask, "status")
		update_mask = append(update_mask, "owner")
	}

	var cc_users []*WireUserType
	for _, cc := range request.CcList {
		cc_users = append(cc_users, &WireUserType{ User: fmt.Sprintf("users/%s", cc) })
	}
	if len(cc_users) != 0 {
		update_mask = append(update_mask, "ccUsers")
	}

	var components []*WireComponentType
	for _, component := range request.Components {
		components = append(
			components,
			&WireComponentType{Component: fmt.Sprintf("projects/%s/componentDefs/%s", request.Project, component)},
		)
	}
	if len(components) != 0 {
		update_mask = append(update_mask, "components")
	}

	wireRequest := &WireRequestType{
		Deltas: []*WireDeltaType{
			&WireDeltaType{ 
				Issue: &WireIssueType{
					Name: fmt.Sprintf("projects/%s/issues/%d", request.Project, request.Crbug),
					Owner: owner,
					CcUsers: cc_users,
					Status: &WireStatusType{ Status: status },
					Components: components,
				},
				UpdateMask: strings.Join(update_mask, ","),
			},
		},
		CommentContent: request.Comment,
		NotifyType: "EMAIL",
	}

	json_request, err := json.Marshal(wireRequest)
	if err != nil {
		return fmt.Errorf("Marshal: %v", err)
	}

	_, err = s.invokeApi([]byte(json_request), "Issues", "ModifyIssues")
	if err != nil {
		return fmt.Errorf("invokeApi: %v", err)
	}
	return nil
}

type CreateIssueRequest struct {
	// "chromium"
	Project string

	Summary     string
	Description string
	Owner				string
	CcList			[]string

	Components []string
}

func (s *IssuesService) CreateIssue(request *CreateIssueRequest) (*Issue, error) {
	type WireComponentType struct {
		Component string `json:"component"`
	}

	type WireStatusType struct {
		Status string `json:"status"`
	}

	type WireUserType struct {
		User string `json:"user"`
	}

	type WireFieldValueType struct {
		Field string `json:"field"`
		Value string `json:"value"`
	}

	type WireIssueType struct {
		Owner				*WireUserType							`json:"owner,omitempty"`
		CcUsers			[]*WireUserType						`json:"cc_users,omitempty"`
		Status      *WireStatusType       `json:"status"`
		Summary     string                `json:"summary"`
		Components  []*WireComponentType  `json:"components"`
		FieldValues []*WireFieldValueType `json:"field_values"`
	}

	type WireRequestType struct {
		Parent      string         `json:"parent"`
		Issue       *WireIssueType `json:"issue"`
		Description string         `json:"description"`
	}

	status := "Untriaged"
	var owner *WireUserType
	if request.Owner != "" {
		owner = &WireUserType{ User: fmt.Sprintf("users/%s", request.Owner) }
		status = "Assigned"
	}

	var cc_users []*WireUserType
	for _, cc := range request.CcList {
		cc_users = append(cc_users, &WireUserType{ User: fmt.Sprintf("users/%s", cc) })
	}

	var components []*WireComponentType
	for _, component := range request.Components {
		components = append(
			components,
			&WireComponentType{Component: fmt.Sprintf("projects/%s/componentDefs/%s", request.Project, component)},
		)
	}

	// Field values ids are here:
	// https://bugs.chromium.org/p/chromium/adminLabels
	// 10 - Type
	// 11 - Pri
	typeField := fmt.Sprintf("projects/%s/fieldDefs/10", request.Project)
	priField := fmt.Sprintf("projects/%s/fieldDefs/11", request.Project)

	wireRequest := &WireRequestType{
		Parent: fmt.Sprintf("projects/%s", request.Project),
		Issue: &WireIssueType{
			Owner: 			owner,
			CcUsers:		cc_users,
			Status:     &WireStatusType{Status: status},
			Summary:    request.Summary,
			Components: components,
			FieldValues: []*WireFieldValueType{
				&WireFieldValueType{Field: priField, Value: "2"},
				&WireFieldValueType{Field: typeField, Value: "Task"},
			},
		},
		Description: request.Description,
	}

	json_request, err := json.Marshal(wireRequest)
	if err != nil {
		return nil, fmt.Errorf("Marshal: %v", err)
	}

	result, err := s.invokeApi([]byte(json_request), "Issues", "MakeIssue")
	if err != nil {
		return nil, fmt.Errorf("invokeApi: %v", err)
	}

	var monorail_issue *monorailIssue
	if err := json.Unmarshal(result, &monorail_issue); err != nil {
		return nil, fmt.Errorf("Unmarshal: %v", err)
	}

	name_parts := strings.Split(monorail_issue.Name, "/")
	id, err := strconv.Atoi(name_parts[len(name_parts)-1])
	if err != nil {
		return nil, fmt.Errorf("Atoi of %s: %v", name_parts[len(name_parts)-1], err)
	}
	return &Issue{Id: id}, nil
}
