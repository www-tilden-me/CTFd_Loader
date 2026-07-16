package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"github.com/vbauerster/mpb/v8"
	"github.com/vbauerster/mpb/v8/decor"
	"golang.org/x/sync/errgroup"
	"io"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
)

type Command string

const (
	CommandPull  Command = "pull"
	CommandScore Command = "score"
	CommandStore Command = "store"
)

type CLIOptions struct {
	Command  Command
	Location string

	BaseURL    string
	BaseURLSet bool

	Cookie    string
	CookieSet bool

	GroupLimit    int
	GroupLimitSet bool
}

const (
	MetadataFilename   = "META.json"
	ScoresFilename     = "SCORE.json"
	DefaultGroupLimit  = 5
	DefaultFolderPerms = 0o755
	DefaultFilePerms   = 0o644
)

type CTFdEvent struct {
	BaseURL                          string
	Headers                          map[string]string
	SuccessfullyDownloadedChallenges map[int]ChallengeInfo
	Location                         string
	Progress                         *mpb.Progress
	GroupLimit                       int
}

type CTFdEventMetadata struct {
	BaseURL                          string            `json:"base_url"`
	Headers                          map[string]string `json:"headers"`
	SuccessfullyDownloadedChallenges []ChallengeInfo   `json:"known_challenges"`
	Location                         string            `json:"location"`
}

func JoinUrl(baseUrl string, parts ...string) (string, error) {
	return url.JoinPath(baseUrl, parts...)
}

func ResolveURL(baseUrl string, reference string) (string, error) {
	base, err := url.Parse(baseUrl)
	if err != nil {
		return "", fmt.Errorf("parse base URL: %w", err)
	}

	ref, err := url.Parse(reference)
	if err != nil {
		return "", fmt.Errorf("parse URL reference %q: %w", reference, err)
	}

	return base.ResolveReference(ref).String(), nil
}

func Get(url string, headers map[string]string) ([]byte, error) {
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}

	for key, value := range headers {
		req.Header.Set(key, value)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	return body, nil
}

type Hint struct {
	Id      int    `json:"id"`
	Cost    int    `json:"cost"`
	Title   string `json:"title"`
	Content string `json:"content"`
}

type DownloadedFile struct {
	Name string
	Data []byte
}

type Challenge struct {
	Id         int    `json:"id"`
	Name       string `json:"name"`
	Value      int    `json:"value"`
	Category   string `json:"category"`
	SolvedByMe bool   `json:"solved_by_me"`
	Attempts   int    `json:"attempts"`
	Ratings    any    `json:"ratings"`

	Description string            `json:"description"`
	Type        string            `json:"type"`
	Files       []*DownloadedFile `json:"-"`
	FilePaths   []string          `json:"files"`
	Hints       []Hint            `json:"hints"`
	MaxAttempts int               `json:"max_attempts"`
}

type ChallengeMetadata struct {
	Id          int      `json:"id"`
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Category    string   `json:"category"`
	Type        string   `json:"type"`
	Files       []string `json:"files"`
	Hints       []Hint   `json:"hints"`
	Ratings     any      `json:"ratings"`
}

type APIResponse struct {
	Success bool `json:"success"`
	Data    any  `json:"data"`
}

type ChallengeInfo struct {
	Id   int    `json:"id"`
	Name string `json:"name"`
}

type Challenges []ChallengeInfo
type FinalScore struct {
	Score      int `json:"score"`
	Total      int `json:"total"`
	Solved     int `json:"solved"`
	Unsolved   int `json:"unsolved"`
	Failed     int `json:"failed"`
	Challenges int `json:"challenge_count"`
}

type ChallengeScores struct {
	Scores      []ChallengeScore `json:"scores"`
	FinalResult FinalScore       `json:"results"`
}

func (ctfd *CTFdEvent) GetChallenges() (*Challenges, error) {
	endpoint, err := JoinUrl(ctfd.BaseURL, "/api/v1/challenges")
	if err != nil {
		return nil, err
	}

	resp, err := Get(endpoint, ctfd.Headers)
	if err != nil {
		return nil, err
	}

	var result APIResponse
	if err := json.NewDecoder(bytes.NewReader(resp)).Decode(&result); err != nil {
		return nil, err
	}

	if !result.Success {
		return nil, fmt.Errorf("API call returned success=false")
	}

	items, ok := result.Data.([]any)
	if !ok {
		return nil, fmt.Errorf(
			"expected API data to be an array, got %T",
			result.Data,
		)
	}

	data := make([]map[string]any, 0, len(items))

	for _, item := range items {
		challenge, ok := item.(map[string]any)
		if !ok {
			return nil, fmt.Errorf(
				"expected challenge to be an object, got %T",
				item,
			)
		}

		data = append(data, challenge)
	}

	raw, err := json.Marshal(data)
	if err != nil {
		return nil, err
	}

	var challenges Challenges
	if err := json.Unmarshal(raw, &challenges); err != nil {
		return nil, err
	}

	return &challenges, nil
}

func (ctfd *CTFdEvent) GetChallenge(id int) (*Challenge, error) {
	endpoint, err := JoinUrl(ctfd.BaseURL, "/api/v1/challenges", strconv.Itoa(id))
	if err != nil {
		return nil, err
	}

	resp, err := Get(endpoint, ctfd.Headers)
	if err != nil {
		return nil, err
	}

	var result APIResponse
	if err := json.NewDecoder(bytes.NewReader(resp)).Decode(&result); err != nil {
		return nil, err
	}

	if !result.Success {
		return nil, fmt.Errorf("API Call success false")
	}

	data, ok := result.Data.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("API Returned Weird Data")
	}

	raw, err := json.Marshal(data)
	if err != nil {
		return nil, err
	}

	challenge := Challenge{}
	if err := json.Unmarshal(raw, &challenge); err != nil {
		return nil, err
	}

	return &challenge, nil
}

func (ctfd *CTFdEvent) DownloadFiles(filePaths []string) ([]*DownloadedFile, error) {
	files := []*DownloadedFile{}

	for _, filePath := range filePaths {
		endpoint, err := ResolveURL(ctfd.BaseURL, filePath)
		if err != nil {
			return nil, err
		}

		resp, err := Get(endpoint, ctfd.Headers)
		if err != nil {
			return nil, err
		}

		parsed, err := url.Parse(filePath)
		if err != nil {
			panic(err)
		}

		filename := path.Base(parsed.Path)

		file := DownloadedFile{
			Name: filename,
			Data: resp,
		}

		files = append(files, &file)
	}

	return files, nil
}

func SanitizeFilename(value string) string {
	value = strings.TrimSpace(value)

	replacer := strings.NewReplacer(
		"/", "",
		"\\", "",
		":", "",
		"*", "",
		"?", "",
		`"`, "",
		"<", "",
		">", "",
		"|", "",
		"'", "",
		"\"", "",
		" ", "_",
	)

	value = replacer.Replace(value)
	value = strings.Trim(value, ". ")

	if value == "" {
		return "unnamed"
	}

	return value
}

func (challenge *Challenge) WriteChallenge(location string) error {
	challengeDir := filepath.Join(
		location,
		SanitizeFilename(challenge.Category),
		SanitizeFilename(challenge.Name),
	)

	if err := os.MkdirAll(challengeDir, DefaultFolderPerms); err != nil {
		return err
	}

	fileNames := make([]string, 0, len(challenge.Files))

	for _, file := range challenge.Files {
		if file == nil {
			continue
		}

		filename := SanitizeFilename(file.Name)
		filePath := filepath.Join(challengeDir, filename)

		if err := os.WriteFile(filePath, file.Data, DefaultFilePerms); err != nil {
			return err
		}

		fileNames = append(fileNames, filename)
	}

	metadata := ChallengeMetadata{
		Id:          challenge.Id,
		Name:        challenge.Name,
		Description: challenge.Description,
		Category:    challenge.Category,
		Type:        challenge.Type,
		Files:       fileNames,
		Hints:       challenge.Hints,
		Ratings:     challenge.Ratings,
	}

	metadataJSON, err := json.MarshalIndent(metadata, "", "  ")
	if err != nil {
		return err
	}

	metadataPath := filepath.Join(challengeDir, MetadataFilename)

	metadataJSON = append(metadataJSON, '\n')

	if err := os.WriteFile(metadataPath, metadataJSON, DefaultFilePerms); err != nil {
		return err
	}

	return nil
}

func (ctfd *CTFdEvent) SaveChallenge(chal ChallengeInfo) error {
	challenge, err := ctfd.GetChallenge(chal.Id)
	if err != nil {
		return fmt.Errorf("%d: %w", chal.Id, err)
	}

	if len(challenge.FilePaths) > 0 {
		files, err := ctfd.DownloadFiles(challenge.FilePaths)
		if err != nil {
			return fmt.Errorf("%d: %w", chal.Id, err)
		}
		challenge.Files = files
	}

	err = challenge.WriteChallenge(ctfd.Location)
	if err != nil {
		return fmt.Errorf("%d: %w", chal.Id, err)
	}
	return nil
}

type ChallengeScore struct {
	Id          int    `json:"id"`
	Name        string `json:"name"`
	Value       int    `json:"value"`
	Category    string `json:"category"`
	SolvedByMe  bool   `json:"solved_by_me"`
	Attempts    int    `json:"attempts"`
	Ratings     any    `json:"ratings"`
	MaxAttempts int    `json:"max_attempts"`
}

func (ctfd *CTFdEvent) ScoreChallenge(chal ChallengeInfo) (*ChallengeScore, error) {
	challenge, err := ctfd.GetChallenge(chal.Id)
	if err != nil {
		return nil, fmt.Errorf("%d: %v", chal.Id, err)
	}

	score := ChallengeScore{
		Id:          challenge.Id,
		Name:        challenge.Name,
		Value:       challenge.Value,
		Category:    challenge.Category,
		SolvedByMe:  challenge.SolvedByMe,
		Attempts:    challenge.Attempts,
		Ratings:     challenge.Ratings,
		MaxAttempts: challenge.MaxAttempts,
	}

	return &score, nil
}

func (ctfd *CTFdEvent) AlreadySaved(challenge ChallengeInfo) bool {
	chal, ok := ctfd.SuccessfullyDownloadedChallenges[challenge.Id]
	if !ok {
		return false
	}
	if chal.Name == challenge.Name {
		return true
	}

	return false
}

func (ctfd *CTFdEvent) PullEvent() {
	challengeList, err := ctfd.GetChallenges()
	if err != nil {
		panic(err)
	}

	bar := ctfd.Progress.AddBar(
		int64(len(*challengeList)),
		mpb.PrependDecorators(
			decor.Name("Loading "),
			decor.CountersNoUnit("%d / %d"),
		),
		mpb.AppendDecorators(
			decor.Percentage(),
			decor.Elapsed(decor.ET_STYLE_GO),
		),
	)

	mutex := sync.Mutex{}
	fails := []ChallengeInfo{}
	var group errgroup.Group

	group.SetLimit(ctfd.GroupLimit)

	for _, chal := range *challengeList {
		if ctfd.AlreadySaved(chal) {
			bar.Increment()
			continue
		}

		group.Go(func() error {
			defer bar.Increment()
			err := ctfd.SaveChallenge(chal)
			if err != nil {
				mutex.Lock()
				fails = append(fails, chal)
				mutex.Unlock()
			} else {
				mutex.Lock()
				ctfd.SuccessfullyDownloadedChallenges[chal.Id] = chal
				mutex.Unlock()
			}

			return nil
		})
	}

	group.Wait()

	if len(fails) > 0 {
		fmt.Println("\nFailed:")
		for _, chal := range fails {
			fmt.Printf("\"%s\" -- id: %d\n", chal.Name, chal.Id)
		}
	}
}

func (scores ChallengeScores) WriteScores(location string) error {
	if err := os.MkdirAll(location, DefaultFolderPerms); err != nil {
		return err
	}

	data, err := json.MarshalIndent(scores, "", "  ")
	if err != nil {
		return err
	}

	data = append(data, '\n')

	metadataPath := filepath.Join(location, ScoresFilename)

	if err := os.WriteFile(metadataPath, data, DefaultFilePerms); err != nil {
		return err
	}

	return nil
}

func (ctfd *CTFdEvent) ScoreEvent() {
	challengeList, err := ctfd.GetChallenges()
	if err != nil {
		panic(err)
	}

	bar := ctfd.Progress.AddBar(
		int64(len(*challengeList)),
		mpb.PrependDecorators(
			decor.Name("Scoring "),
			decor.CountersNoUnit("%d / %d"),
		),
		mpb.AppendDecorators(
			decor.Percentage(),
			decor.Elapsed(decor.ET_STYLE_GO),
		),
	)

	var group errgroup.Group

	group.SetLimit(ctfd.GroupLimit)

	mutex := sync.Mutex{}
	scores := ChallengeScores{}
	scores.FinalResult = FinalScore{}

	for _, chal := range *challengeList {
		group.Go(func() error {
			defer bar.Increment()
			score, err := ctfd.ScoreChallenge(chal)
			if err != nil {
				return fmt.Errorf("Could not score \"%s\"", chal.Name)
			}

			mutex.Lock()
			scores.FinalResult.Challenges += 1
			scores.Scores = append(scores.Scores, *score)
			scores.FinalResult.Total += score.Value
			if score.SolvedByMe {
				scores.FinalResult.Solved += 1
				scores.FinalResult.Score += score.Value
			} else if score.MaxAttempts > 0 && score.Attempts >= score.MaxAttempts {
				scores.FinalResult.Failed += 1
			} else {
				scores.FinalResult.Unsolved += 1
			}
			mutex.Unlock()
			return nil
		})
	}

	if err := group.Wait(); err != nil {
		panic(err)
	}

	if err = scores.WriteScores(ctfd.Location); err != nil {
		panic(err)
	}

	fmt.Printf("\nSolved: %d / %d\nUnsolved: %d / %d\nFailed: %d / %d\nScore: %d / %d\n",
		scores.FinalResult.Solved,
		scores.FinalResult.Challenges,
		scores.FinalResult.Unsolved,
		scores.FinalResult.Challenges,
		scores.FinalResult.Failed,
		scores.FinalResult.Challenges,
		scores.FinalResult.Score,
		scores.FinalResult.Total)
}

func (ctfd *CTFdEvent) GetSuccessfullyDownloadedChallengeList() []ChallengeInfo {
	result := []ChallengeInfo{}
	for _, value := range ctfd.SuccessfullyDownloadedChallenges {
		result = append(result, value)
	}
	return result
}

func (ctfd *CTFdEvent) WriteMetadata() error {
	if err := os.MkdirAll(ctfd.Location, DefaultFolderPerms); err != nil {
		return fmt.Errorf("create event directory %q: %w", ctfd.Location, err)
	}

	metadata := CTFdEventMetadata{
		BaseURL:                          ctfd.BaseURL,
		Headers:                          ctfd.Headers,
		SuccessfullyDownloadedChallenges: ctfd.GetSuccessfullyDownloadedChallengeList(),
		Location:                         ctfd.Location,
	}

	data, err := json.MarshalIndent(metadata, "", "  ")
	if err != nil {
		return fmt.Errorf("encode event metadata: %w", err)
	}

	data = append(data, '\n')

	metadataPath := filepath.Join(metadata.Location, MetadataFilename)

	if err := os.WriteFile(metadataPath, data, DefaultFilePerms); err != nil {
		return fmt.Errorf("write event metadata %q: %w", metadataPath, err)
	}

	return nil
}

func ReadEventMetadata(location string) (*CTFdEvent, error) {
	absoluteLocation, err := filepath.Abs(location)
	if err != nil {
		return nil, fmt.Errorf("resolve event location %q: %w", location, err)
	}

	metadataPath := filepath.Join(
		absoluteLocation,
		MetadataFilename,
	)

	data, err := os.ReadFile(metadataPath)
	if err != nil {
		return nil, fmt.Errorf("read event metadata %q: %w", metadataPath, err)
	}

	var metadata CTFdEventMetadata

	if err := json.Unmarshal(data, &metadata); err != nil {
		return nil, fmt.Errorf("decode event metadata %q: %w", metadataPath, err)
	}

	event := CTFdEvent{
		BaseURL:                          metadata.BaseURL,
		Headers:                          metadata.Headers,
		SuccessfullyDownloadedChallenges: make(map[int]ChallengeInfo),
		Location:                         metadata.Location,
	}

	for _, chal := range metadata.SuccessfullyDownloadedChallenges {
		event.SuccessfullyDownloadedChallenges[chal.Id] = chal
	}

	return &event, nil
}

func ParseCLI() (*CLIOptions, error) {
	if len(os.Args) < 3 {
		return nil, fmt.Errorf(
			"usage: %s <score|pull|store> <location> [--base URL] [--cookie COOKIE] [--group_limit N]",
			os.Args[0],
		)
	}

	command := Command(os.Args[1])
	location := os.Args[2]

	switch command {
	case CommandPull, CommandScore, CommandStore:
	default:
		return nil, fmt.Errorf(
			"invalid command %q: expected score, pull, or store",
			command,
		)
	}

	flags := flag.NewFlagSet(string(command), flag.ContinueOnError)

	baseURL := flags.String(
		"base",
		"",
		"base URL of the CTFd instance",
	)

	cookie := flags.String(
		"cookie",
		"",
		"CTFd session cookie",
	)

	groupLimit := flags.Int(
		"group_limit",
		DefaultGroupLimit,
		"maximum number of concurrent requests",
	)

	if err := flags.Parse(os.Args[3:]); err != nil {
		return nil, err
	}

	if flags.NArg() > 0 {
		return nil, fmt.Errorf(
			"unexpected positional arguments: %v",
			flags.Args(),
		)
	}

	// Record which flags were explicitly passed.
	present := make(map[string]bool)

	flags.Visit(func(f *flag.Flag) {
		present[f.Name] = true
	})

	if present["group_limit"] && *groupLimit < 1 {
		return nil, fmt.Errorf(
			"--group_limit must be at least 1, got %d",
			*groupLimit,
		)
	}

	absoluteLocation, err := filepath.Abs(location)
	if err != nil {
		return nil, fmt.Errorf(
			"resolve directory %q: %w",
			location,
			err,
		)
	}

	return &CLIOptions{
		Command:  command,
		Location: absoluteLocation,

		BaseURL:    strings.TrimRight(*baseURL, "/"),
		BaseURLSet: present["base"],

		Cookie:    *cookie,
		CookieSet: present["cookie"],

		GroupLimit:    *groupLimit,
		GroupLimitSet: present["group_limit"],
	}, nil
}

func main() {
	options, err := ParseCLI()
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(2)
	}

	// First try loading the existing event from the positional directory.
	event, err := ReadEventMetadata(options.Location)
	if err != nil {
		// No usable metadata, so start with defaults.
		event = &CTFdEvent{
			BaseURL:                          "",
			Headers:                          make(map[string]string),
			Location:                         options.Location,
			GroupLimit:                       DefaultGroupLimit,
			SuccessfullyDownloadedChallenges: make(map[int]ChallengeInfo),
		}
	}

	// The positional location always wins.
	event.Location = options.Location

	if options.BaseURLSet {
		event.BaseURL = options.BaseURL
	}

	if options.CookieSet {
		if event.Headers == nil {
			event.Headers = make(map[string]string)
		}

		event.Headers["Cookie"] = options.Cookie
	}

	event.GroupLimit = options.GroupLimit
	if event.GroupLimit < 1 {
		event.GroupLimit = DefaultGroupLimit
	}

	if event.BaseURL == "" {
		fmt.Fprintln(os.Stderr, "No Base URL")
		os.Exit(1)
	}

	event.Progress = mpb.New()
	defer event.Progress.Wait()

	switch options.Command {
	case CommandPull:
		event.PullEvent()

		if err := event.WriteMetadata(); err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
			os.Exit(1)
		}

	case CommandScore:
		event.ScoreEvent()
	case CommandStore:
		if err := event.WriteMetadata(); err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
			os.Exit(1)
		}
	}
}
