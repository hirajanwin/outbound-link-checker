package main

import (
	"fmt"
	"strings"
	"net/http"
	"io/ioutil"
	"net/url"
	"regexp"
	"sync"
	"time"
	"bufio"
	"os"
	"flag"
	)

const defaultCrawlPageLimit = -1 // Unlimited
const defaultMaxConcurrentCrawls = 20
const defaultMaxBodyFetchRetryCount = 3
const defaultDomainWhitelistFile = "./whitelisted_domains.txt"

var crawlPageLimit = flag.Int("num-crawl-url-limit", defaultCrawlPageLimit,
	"Number of urls to crawl (default: unlimited)")
var maxConcurrentCrawls = flag.Int("num-concurrent-crawls", defaultMaxConcurrentCrawls,
	fmt.Sprintf("Number of concurrent requests to the website (Default: %d)", defaultMaxConcurrentCrawls))
var maxBodyFetchRetryCount = flag.Int("num-retry", defaultMaxBodyFetchRetryCount,
	fmt.Sprintf("Number of retry attempts to fetch a URL (Default: %d)", defaultMaxBodyFetchRetryCount))
var interactive = flag.Bool("interactive", true, "Allows you to interactively add new domains to the list as they"+
	" are encountered")
var domainWhitelistFile = flag.String("domains-whitelist-file",
	defaultDomainWhitelistFile,
	"A file containing a new-line separated white-listed domains,"+
		" links to these domains will be ignored, any empty lines or lines starting with \"//\" in"+
		" this file will be ignored as well")
var startingUrl = flag.String("starting-url", "",
	"The starting url to start the crawl from. Usually, the URL of the homepage"+
		", for example, https://ashishb.net")
var domain = flag.String("domain", "",
	"The domain of the website, everything not on this domain will be considered outbound,"+
		" don't prefix www in the front, for example, ashishb.net")

func main() {
	handleFlags()

	whitelistedDomains := initWhitelistedDomains()
	// Maps url1 -> url2 if url1 has a link to url2.
	outboundLinkMap := make(map[string][]string, 0)
	visitedMap := make(map[string]bool, 0)
	crawl(*startingUrl, *domain, outboundLinkMap, visitedMap, *crawlPageLimit)
	printResults(outboundLinkMap, *domain, whitelistedDomains)
}

func handleFlags() {
	flag.Parse()
	if len(*domain) == 0 {
		panic("Missing domain parameter")
	}
	if len(*startingUrl) == 0 {
		panic("Missing starting url")
	}
}

func initWhitelistedDomains() map[string]bool {
	dat, err := ioutil.ReadFile(*domainWhitelistFile)
	if err != nil {
		panic(fmt.Sprintf("Error reading the domain whitelist file: %s\n", domainWhitelistFile))
	}
	whitelisted := make([]string, 0)
	whitelistCount := 0
	for _, line := range strings.Split(string(dat), "\n") {
		line = strings.Trim(line, " ")
		// Ignore empty lines
		if len(line) == 0 {
			continue
		}
		// Ignore comments
		if strings.HasPrefix(line, "//") {
			continue
		}
		whitelistCount += 1
		whitelisted = append(whitelisted, line)
	}
	fmt.Printf("Read %d domains in the domain whitelist\n", whitelistCount)

	whitelistedDomains := make(map[string]bool, 0)
	for _, domain := range whitelisted {
		addDomainToWhiteList(whitelistedDomains, domain)
	}
	return whitelistedDomains
}

func addDomainToWhiteList(whitelistedDomains map[string]bool, domain string) {
	whitelistedDomains[domain] = true
	whitelistedDomains["www."+domain] = true
}

// Global variables
var count = 0
var lock = sync.Mutex{}
var crawlCountLock = sync.Mutex{}
var runningCrawlCount = 0

func crawl(
	url string,
	domain string,
	outboundLinkMap map[string][]string,
	visitedMap map[string]bool,
	crawlPageLimit int) {

	if hasVisited(url, visitedMap) {
		// fmt.Printf("Skipping already visited url: %s\n", url)
		return
	}

	lock.Lock()
	count += 1
	countValue := count
	// Code breaker for testing
	if crawlPageLimit > 0 && countValue > crawlPageLimit {
		lock.Unlock()
		return
	} else {
		lock.Unlock()
	}

	fmt.Printf("Crawling %d (limit: %d) URL: \"%s\"\n", countValue, crawlPageLimit, url)

	// Fetch the body
	body, err := getBody(url)
	if err != nil {
		fmt.Printf("Error %s while crawling url %s\n", err, url)
		return
	}

	recordVisit(url, visitedMap)

	// Extract the urls
	urls := getUrls(body)

	for _, url2 := range urls {
		url2 = normalizeUrl(url2)
		recordLink(url, url2, outboundLinkMap)
		result, err := belongsToDomain(url2, domain)
		if err != nil {
			fmt.Printf("Error %s while parsing %s\n", err, url2)
			continue
		}
		if result {
			go crawl(url2, domain, outboundLinkMap, visitedMap, crawlPageLimit)
		}
	}

	time.Sleep(time.Second)
	for {
		crawlCountLock.Lock()
		value := runningCrawlCount
		crawlCountLock.Unlock()
		if value > 0 {
			//fmt.Printf("Running count value is %d\n", value)
			time.Sleep(time.Second)
		} else {
			return
		}
	}
}
func waitForCrawlCountAvailability() {
	for {
		crawlCountLock.Lock()
		value := runningCrawlCount
		crawlCountLock.Unlock()
		if value < *maxConcurrentCrawls {
			return
		}
	}
}
func incrementRunningCrawlCount() {
	crawlCountLock.Lock()
	runningCrawlCount += 1
	crawlCountLock.Unlock()
}

func decrementRunningCrawlCount() {
	crawlCountLock.Lock()
	runningCrawlCount -= 1
	crawlCountLock.Unlock()
}

func recordLink(url string, url2 string, outboundLinkMap map[string][]string) {
	lock.Lock()
	defer lock.Unlock()
	url = normalizeUrl(url)
	if outboundLinkMap[url] == nil {
		outboundLinkMap[url] = make([]string, 0)
	}
	outboundLinkMap[url] = append(outboundLinkMap[url], url2)
}

func hasVisited(url string, visitedMap map[string]bool) (bool) {
	lock.Lock()
	defer lock.Unlock()
	url = normalizeUrl(url)
	return visitedMap[url]
}

func normalizeUrl(url string) string {
	// Remove bookmark fragments.
	if strings.Contains(url, "#") {
		url = strings.Split(url, "#")[0]
	}

	//// Remove trailing forward slash.
	//if strings.HasSuffix(url, "/") {
	//	url = url[0:len(url) - 1]
	//}
	return url
}

func recordVisit(url string, visitedMap map[string]bool) {
	lock.Lock()
	defer lock.Unlock()
	visitedMap[url] = true
}

func belongsToDomain(url2 string, domain string) (bool, error) {
	parsedUrl, err := url.Parse(url2)
	if err != nil {
		return false, err
	}
	hostname := parsedUrl.Host
	if strings.Compare(hostname, domain) == 0 {
		return true, nil
	}
	if strings.Compare(hostname, "www."+domain) == 0 {
		return true, nil
	}
	return false, nil
}

func getBody(url string) (string, error) {
	waitForCrawlCountAvailability()
	incrementRunningCrawlCount()
	defer decrementRunningCrawlCount()

	var err error
	retryCount := 0
	for retryCount < *maxBodyFetchRetryCount {
		retryCount += 1
		time.Sleep(time.Duration((retryCount - 1) * 1000 * 1000 * 1000))
		response, err1 := http.Get(url)
		if err1 != nil {
			fmt.Printf("Failed to fetch on %d try: %s\n", retryCount, url)
			err = err1
			continue
		}
		bodyBytes, err2 := ioutil.ReadAll(response.Body)
		if err2 != nil {
			fmt.Printf("Failed to fetch on %d try: %s\n", retryCount, url)
			err = err2
			continue
		}
		return string(bodyBytes), nil
	}
	return "", err
}

// Hacky way to get links from HTML page
var linkRegEx = regexp.MustCompile("<a href=['\"](.*?)['\"]")

func getUrls(htmlBody string) []string {
	links := linkRegEx.FindAllStringSubmatch(htmlBody, -1)
	result := make([]string, len(links))
	for i := range links {
		result = append(result, links[i][1])
	}
	return result
}

func printResults(
	outboundLinkMap map[string][]string,
	domain string,
	whitelistedDomains map[string]bool) {
	link := make(map[string][]string, 0)
	for url1, urls := range outboundLinkMap {
		for _, url2 := range urls {
			//parsedUrl2, err := url.Parse(url2)
			//if err != nil {
			//	fmt.Println("Error parsing url %s: %s\n", url2, err)
			//	continue
			//}
			//url2Host := parsedUrl2.Host

			result, _ := belongsToDomain(url2, domain)
			if result {
				continue
			}
			result2, _ := belongsToWhitelistedDomains(url2, whitelistedDomains)
			if result2 {
				continue
			}
			link[url2] = append(link[url2], url1)
		}
	}

	fmt.Printf("Results:\n")
	count := 0
	for url, source_urls := range link {
		if len(source_urls) >= 1 {
			count += 1
			fmt.Printf("URL %d: %s\ninbound pages: %s\n\n", count, url, source_urls[0])
			if *interactive {
				handleInteractively(url, whitelistedDomains)
			}
		}
	}
}

func belongsToWhitelistedDomains(url2 string, whitelistedDomains map[string]bool) (bool, error) {
	parsedUrl, err := url.Parse(url2)
	if err != nil {
		return false, err
	}
	return whitelistedDomains[parsedUrl.Host], nil
}

// Whitelists domains interactively
func handleInteractively(url2 string, whitelistedDomains map[string]bool) {
	url2 = strings.Trim(url2, " ")
	parsedUrl, err := url.Parse(url2)
	if err != nil {
		fmt.Printf("Error parsing \"%s\" to extract domain\n", url2)
		return
	}

	domain := parsedUrl.Host
	if strings.HasPrefix(domain, "www.") {
		// Remove the "www." prefix
		domain = domain[4:]
	}
	if len(domain) == 0 {
		return
	}

	// Domain was whitelisted in this pass
	if whitelistedDomains[domain] {
		return
	}

	fmt.Printf("Whitelist domain \"%s\" [y/N]?", domain)
	reader := bufio.NewReader(os.Stdin)
	text, _ := reader.ReadString('\n')
	if strings.Compare(text, "y\n") == 0 {
		addDomainToWhiteList(whitelistedDomains, domain)
		file, _ := os.OpenFile(*domainWhitelistFile, os.O_APPEND|os.O_WRONLY, 0600)
		defer file.Close()
		w := bufio.NewWriter(file)
		fmt.Fprintln(w, domain)
		w.Flush()
		fmt.Printf("Domain %s whitelisted\n\n", domain)
	} else {
		fmt.Printf("Domain %s not whitelisted\n\n", domain)
	}
}