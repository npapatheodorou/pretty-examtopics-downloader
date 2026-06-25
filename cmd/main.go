package main

import (
	"bufio"
	"flag"
	"fmt"
	"math"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"examtopics-downloader/internal/fetch"
	"examtopics-downloader/internal/utils"
)

const (
	ansiReset  = "\x1b[0m"
	ansiBold   = "\x1b[1m"
	ansiCyan   = "\x1b[36m"
	ansiGreen  = "\x1b[32m"
	ansiYellow = "\x1b[33m"
	ansiRed    = "\x1b[31m"
	ansiGray   = "\x1b[90m"
)

var useANSI = detectANSI()

func main() {
	defer func() {
		if r := recover(); r != nil {
			printErrorf("Unexpected error: %v\n", r)
			pauseBeforeExitOnError()
			os.Exit(1)
		}
	}()

	if err := run(); err != nil {
		printErrorf("Error: %v\n", err)
		pauseBeforeExitOnError()
		os.Exit(1)
	}
}

func run() error {
	debug := flag.Bool("debug", false, "Enable debug logs")
	noCache := flag.Bool("no-cache", false, "Bypass the on-disk question-page cache (always fetch fresh)")
	recentDays := flag.Int("recent-comments-days", 0, "Keep only questions with a comment in the last N days (0 = ask interactively / disabled)")
	flag.Parse()
	fetch.SetDebug(*debug)
	fetch.SetCacheEnabled(!*noCache)

	// Distinguish "flag explicitly passed" from "left at default 0" so we only
	// fall back to the interactive prompt when the user didn't specify a window.
	recentDaysSet := false
	flag.Visit(func(f *flag.Flag) {
		if f.Name == "recent-comments-days" {
			recentDaysSet = true
		}
	})

	printBanner()

	reader := bufio.NewReader(os.Stdin)

	selectedProvider, err := promptSelectionWithRefresh(
		reader,
		"Available Providers",
		getProvidersWithStatus,
		formatProviderName,
	)
	if err != nil {
		return fmt.Errorf("failed reading provider selection: %w", err)
	}

	includeDiscussionExamDiscovery, err := promptYesNo(
		reader,
		fmt.Sprintf("Include discussion pages while discovering exams for %s? [y/N]: ", formatProviderName(selectedProvider)),
		false,
	)
	if err != nil {
		return fmt.Errorf("failed reading exam discovery mode: %w", err)
	}

	selectedExam, err := promptSelectionWithRefresh(
		reader,
		fmt.Sprintf("Available Exams for %s", formatProviderName(selectedProvider)),
		func() []string {
			return getProviderExamSlugsWithStatus(selectedProvider, includeDiscussionExamDiscovery)
		},
		func(s string) string {
			if s == "all-discussions" {
				return "all-discussions (fallback)"
			}
			return s
		},
	)
	if err != nil {
		if !includeDiscussionExamDiscovery && strings.Contains(strings.ToLower(err.Error()), "no options found") {
			return fmt.Errorf("no official exams were found for %s. Run again and answer 'y' to include discussion pages", formatProviderName(selectedProvider))
		}
		return fmt.Errorf("failed reading exam selection: %w", err)
	}
	extractionFilter := selectedExam
	if selectedExam == "all-discussions" {
		extractionFilter = ""
	}

	// Resolve the recent-comment window: an explicit flag wins; otherwise ask.
	recentWindowDays := *recentDays
	if !recentDaysSet {
		recentWindowDays, err = promptRecentCommentsDays(reader)
		if err != nil {
			return fmt.Errorf("failed reading recent-comments window: %w", err)
		}
	}

	printInfof("Starting extraction for %s / %s...\n", formatProviderName(selectedProvider), selectedExam)
	links := fetch.GetAllPages(selectedProvider, extractionFilter)
	if len(links) == 0 {
		return fmt.Errorf("no matching questions were extracted")
	}

	if recentWindowDays > 0 {
		before := len(links)
		links = utils.FilterByRecentComments(links, recentWindowDays, time.Now())
		printInfof("Recent-comment filter (last %d days): kept %d, dropped %d of %d question(s).\n",
			recentWindowDays, len(links), before-len(links), before)
		if len(links) == 0 {
			return fmt.Errorf("no questions had a comment within the last %d days; rerun with a larger --recent-comments-days value or 0 to disable", recentWindowDays)
		}
	}

	outputPath := defaultOutputPath(selectedProvider, selectedExam)
	headerExam := selectedExam
	if selectedExam == "all-discussions" {
		headerExam = ""
	}
	savedFiles, err := utils.WriteDataWithSelection(links, outputPath, true, selectedProvider, headerExam)
	if err != nil {
		return fmt.Errorf("failed writing output: %w", err)
	}

	printSuccessf("Successfully saved output: %s\n", strings.Join(savedFiles, ", "))
	return nil
}

func pauseBeforeExitOnError() {
	stat, err := os.Stdin.Stat()
	if err != nil {
		return
	}
	if (stat.Mode() & os.ModeCharDevice) == 0 {
		return
	}

	fmt.Print(style("Press Enter to close...", ansiGray))
	_, _ = bufio.NewReader(os.Stdin).ReadString('\n')
}

func getProvidersWithStatus() []string {
	printInfof("Loading providers from ExamTopics...\n")
	fmt.Println(style("This may take a moment while data is fetched from exams and discussions.", ansiGray))

	done := make(chan struct{})
	start := time.Now()

	go func() {
		ticker := time.NewTicker(15 * time.Second)
		defer ticker.Stop()

		status := []string{
			"Still working: loading provider categories...",
			"Still working: checking discussions-only providers...",
			"Still working: organizing provider list...",
		}
		step := 0

		for {
			select {
			case <-done:
				return
			case <-ticker.C:
				elapsed := time.Since(start).Round(time.Second)
				printInfof("%s Elapsed: %s\n", status[step], elapsed)
				if step < len(status)-1 {
					step++
				}
			}
		}
	}()

	providers := fetch.GetAllProviders()
	close(done)

	elapsed := time.Since(start).Round(time.Second)
	printSuccessf("Done. Found %d provider(s) in %s.\n", len(providers), elapsed)
	return providers
}

func getProviderExamSlugsWithStatus(provider string, includeDiscussionExamDiscovery bool) []string {
	providerLabel := formatProviderName(provider)
	printSection(fmt.Sprintf("Exam Discovery: %s", providerLabel))
	if includeDiscussionExamDiscovery {
		fmt.Println(style("Scanning available exams (including discussion-derived variants).", ansiGray))
	} else {
		fmt.Println(style("Scanning available exams from the official provider exam list only.", ansiGray))
	}

	done := make(chan struct{})
	start := time.Now()

	go func() {
		ticker := time.NewTicker(20 * time.Second)
		defer ticker.Stop()

		status := []string{
			"Still working: checking available exam names...",
			"Still working: loading the provider exam list...",
			"Still working: organizing the exam list for you.",
		}
		if includeDiscussionExamDiscovery {
			status[1] = "Still working: this provider has many discussion pages to review."
		}
		step := 0

		for {
			select {
			case <-done:
				return
			case <-ticker.C:
				elapsed := time.Since(start).Round(time.Second)
				printInfof("%s Elapsed: %s\n", status[step], elapsed)
				if step < len(status)-1 {
					step++
				}
			}
		}
	}()

	examSlugs := fetch.GetProviderExamSlugs(provider, includeDiscussionExamDiscovery)
	close(done)

	elapsed := time.Since(start).Round(time.Second)
	printSuccessf("Done. Found %d exam option(s) for %s in %s.\n", len(examSlugs), providerLabel, elapsed)

	return examSlugs
}

func promptYesNo(reader *bufio.Reader, prompt string, defaultYes bool) (bool, error) {
	for {
		fmt.Print(style(prompt, ansiBold+ansiCyan))

		raw, err := reader.ReadString('\n')
		if err != nil {
			return false, err
		}

		answer := strings.ToLower(strings.TrimSpace(raw))
		if answer == "" {
			return defaultYes, nil
		}

		switch answer {
		case "y", "yes":
			return true, nil
		case "n", "no":
			return false, nil
		default:
			printWarnf("Please answer y or n.\n")
		}
	}
}

// promptRecentCommentsDays asks for an optional "recent comment activity"
// window in days. Blank input means "keep all" (returns 0). Re-prompts on
// non-numeric or negative input.
func promptRecentCommentsDays(reader *bufio.Reader) (int, error) {
	prompt := "Keep only questions with a comment in the last N days? (e.g. 180 = ~6 months, 365 = ~12 months; blank = keep all): "
	for {
		fmt.Print(style(prompt, ansiBold+ansiCyan))

		raw, err := reader.ReadString('\n')
		if err != nil {
			return 0, err
		}

		answer := strings.TrimSpace(raw)
		if answer == "" {
			return 0, nil
		}

		days, err := strconv.Atoi(answer)
		if err != nil || days < 0 {
			printWarnf("Please enter a positive number of days, or leave blank to keep all.\n")
			continue
		}
		return days, nil
	}
}

func promptSelectionWithRefresh(
	reader *bufio.Reader,
	title string,
	loadOptions func() []string,
	formatter func(string) string,
) (string, error) {
	options := loadOptions()
	if len(options) == 0 {
		return "", fmt.Errorf("no options found for %s", title)
	}

	filter := ""
	for {
		all := make([]selectionOption, 0, len(options))
		for i, opt := range options {
			all = append(all, selectionOption{
				RawIndex: i,
				Label:    formatter(opt),
			})
		}

		filtered := filterOptions(all, filter)
		printMenuHeader(title, len(filtered), len(all), filter)
		if len(filtered) == 0 {
			printWarnf("No results for filter %q. Use / to clear.\n", filter)
		} else {
			printOptionsInColumns(filtered)
		}
		printMenuHelp()

		fmt.Print(style("Select> ", ansiBold+ansiCyan))
		raw, err := reader.ReadString('\n')
		if err != nil {
			return "", err
		}

		raw = strings.TrimSpace(raw)
		if raw == "" {
			continue
		}

		if strings.HasPrefix(raw, "/") {
			command := strings.TrimSpace(strings.TrimPrefix(raw, "/"))
			switch strings.ToLower(command) {
			case "":
				filter = ""
			case "refresh":
				printInfof("Refreshing list...\n")
				refreshed := loadOptions()
				if len(refreshed) == 0 {
					printWarnf("Refresh returned no results. Keeping current list.\n")
				} else {
					options = refreshed
					filter = ""
					printSuccessf("List refreshed. %d option(s) available.\n", len(options))
				}
			default:
				filter = command
			}
			continue
		}

		choice, err := strconv.Atoi(raw)
		if err != nil || choice < 1 || choice > len(filtered) {
			printWarnf("Invalid selection. Please enter a valid number.\n")
			continue
		}

		return options[filtered[choice-1].RawIndex], nil
	}
}

type selectionOption struct {
	RawIndex int
	Label    string
}

func filterOptions(options []selectionOption, filter string) []selectionOption {
	if strings.TrimSpace(filter) == "" {
		return options
	}

	filter = strings.ToLower(strings.TrimSpace(filter))
	filtered := make([]selectionOption, 0, len(options))
	for _, opt := range options {
		if strings.Contains(strings.ToLower(opt.Label), filter) {
			filtered = append(filtered, opt)
		}
	}
	return filtered
}

func printOptionsInColumns(options []selectionOption) {
	if len(options) == 0 {
		return
	}

	lines := make([]string, 0, len(options))
	maxWidth := 0
	for i, opt := range options {
		line := fmt.Sprintf("%3d. %s", i+1, strings.TrimSpace(opt.Label))
		lines = append(lines, line)
		if len(line) > maxWidth {
			maxWidth = len(line)
		}
	}

	colWidth := maxWidth + 4
	if colWidth < 24 {
		colWidth = 24
	}
	targetWidth := 120
	cols := targetWidth / colWidth
	if cols < 1 {
		cols = 1
	}
	if cols > 4 {
		cols = 4
	}

	rows := int(math.Ceil(float64(len(lines)) / float64(cols)))
	for r := 0; r < rows; r++ {
		var row strings.Builder
		for c := 0; c < cols; c++ {
			idx := c*rows + r
			if idx >= len(lines) {
				continue
			}
			if c > 0 {
				row.WriteString("  ")
			}
			row.WriteString(style(fmt.Sprintf("%-*s", colWidth, lines[idx]), ansiCyan))
		}
		fmt.Println(strings.TrimRight(row.String(), " "))
	}
}

func printBanner() {
	fmt.Println(style(strings.Repeat("=", 64), ansiGray))
	fmt.Println(style(" ExamTopics Downloader - Interactive Exam Extractor", ansiBold+ansiCyan))
	fmt.Println(style(strings.Repeat("=", 64), ansiGray))
	fmt.Println()
}

func printSection(title string) {
	fmt.Println()
	fmt.Println(style(strings.Repeat("-", 64), ansiGray))
	fmt.Println(style(" "+title, ansiBold+ansiCyan))
	fmt.Println(style(strings.Repeat("-", 64), ansiGray))
}

func printMenuHeader(title string, shown int, total int, filter string) {
	printSection(title)
	fmt.Println(style(fmt.Sprintf(" Showing %d of %d", shown, total), ansiGray))
	if strings.TrimSpace(filter) != "" {
		fmt.Println(style(fmt.Sprintf(" Filter: %q", filter), ansiYellow))
	}
	fmt.Println()
}

func printMenuHelp() {
	fmt.Println(style(" Commands: [number] select | /text filter | / clear | /refresh refetch", ansiGray))
}

func printInfof(format string, args ...any) {
	fmt.Printf(style("[INFO] ", ansiCyan)+format, args...)
}

func printSuccessf(format string, args ...any) {
	fmt.Printf(style("[OK] ", ansiGreen)+format, args...)
}

func printWarnf(format string, args ...any) {
	fmt.Printf(style("[WARN] ", ansiYellow)+format, args...)
}

func printErrorf(format string, args ...any) {
	fmt.Printf(style("[ERROR] ", ansiRed)+format, args...)
}

func style(text string, code string) string {
	if !useANSI || text == "" {
		return text
	}
	return code + text + ansiReset
}

func detectANSI() bool {
	stat, err := os.Stdout.Stat()
	if err != nil {
		return false
	}
	if (stat.Mode() & os.ModeCharDevice) == 0 {
		return false
	}
	if strings.EqualFold(strings.TrimSpace(os.Getenv("NO_COLOR")), "1") {
		return false
	}
	term := strings.TrimSpace(strings.ToLower(os.Getenv("TERM")))
	if term == "dumb" {
		return false
	}
	return true
}

func defaultOutputPath(provider, examSlug string) string {
	baseProvider := sanitizeFilenameSegment(provider)
	baseExamCode := sanitizeFilenameSegment(examSlug)
	if baseProvider == "" {
		baseProvider = "examtopics"
	}
	if baseExamCode == "" {
		baseExamCode = "output"
	}

	return fmt.Sprintf("%s_%s.html", baseProvider, baseExamCode)
}

func sanitizeFilenameSegment(input string) string {
	segment := strings.TrimSpace(strings.ToLower(input))
	if segment == "" {
		return ""
	}

	segment = strings.ReplaceAll(segment, " ", "-")
	invalidChars := regexp.MustCompile(`[^a-z0-9._-]+`)
	segment = invalidChars.ReplaceAllString(segment, "-")
	segment = strings.Trim(segment, "-._")

	return segment
}

func formatProviderName(provider string) string {
	provider = strings.TrimSpace(strings.ToLower(provider))
	if provider == "" {
		return "Unknown"
	}

	overrides := map[string]string{
		"aws":                "AWS",
		"ec-council":         "EC-Council",
		"eccouncil":          "EC-Council",
		"isc2":               "ISC2",
		"isaca":              "ISACA",
		"paloalto-networks":  "Palo Alto Networks",
		"palo-alto-networks": "Palo Alto Networks",
		"servicenow":         "ServiceNow",
		"vmware":             "VMware",
		"lpi":                "LPI",
	}
	if label, ok := overrides[provider]; ok {
		return label
	}

	parts := strings.Fields(strings.ReplaceAll(provider, "-", " "))
	for i, part := range parts {
		if part == "" {
			continue
		}
		parts[i] = strings.ToUpper(part[:1]) + part[1:]
	}
	return strings.Join(parts, " ")
}
