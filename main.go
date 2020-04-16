package main

import (
	"bufio"
	"crypto/tls"
	"flag"
	"fmt"
	"io"
	"io/ioutil"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"
)

type probeArgs []string

func (p *probeArgs) Set(val string) error {
	*p = append(*p, val)
	return nil
}

func (p probeArgs) String() string {
	return strings.Join(p, ",")
}

func main() {

	// concurrency flag
	var concurrency int
	flag.IntVar(&concurrency, "c", 20, "set the concurrency level (split equally between HTTPS and HTTP requests)")

	// probe flags
	var probes probeArgs
	flag.Var(&probes, "p", "add additional probe (proto:port)")

	// skip default probes flag
	var skipDefault bool
	flag.BoolVar(&skipDefault, "s", false, "skip the default probes (http:80 and https:443)")

	// timeout flag
	var to int
	flag.IntVar(&to, "t", 10000, "timeout (milliseconds)")

	// prefer https
	var preferHTTPS bool
	flag.BoolVar(&preferHTTPS, "prefer-https", false, "only try plain HTTP if HTTPS fails")

	// HTTP method to use
	var method string
	flag.StringVar(&method, "method", "GET", "HTTP method to use")

	flag.Parse()

	// make an actual time.Duration out of the timeout
	timeout := time.Duration(to * 1000000)

	var tr = &http.Transport{
		MaxIdleConns:      30,
		IdleConnTimeout:   time.Second,
		DisableKeepAlives: true,
		TLSClientConfig:   &tls.Config{InsecureSkipVerify: true},
		DialContext: (&net.Dialer{
			Timeout:   timeout,
			KeepAlive: time.Second,
		}).DialContext,
	}

	re := func(req *http.Request, via []*http.Request) error {
		return http.ErrUseLastResponse
	}

	client := &http.Client{
		Transport:     tr,
		CheckRedirect: re,
		Timeout:       timeout,
	}

	// domain/port pairs are initially sent on the httpsURLs channel.
	// If they are listening and the --prefer-https flag is set then
	// no HTTP check is performed; otherwise they're put onto the httpURLs
	// channel for an HTTP check.
	httpsURLs := make(chan string)
	httpURLs := make(chan string)
	output := make(chan string)

	// HTTPS workers
	var httpsWG sync.WaitGroup
	for i := 0; i < concurrency/2; i++ {
		httpsWG.Add(1)

		go func() {
			for url := range httpsURLs {

				// always try HTTPS first
				withProto := "https://" + url
				rdr := isListening(client, withProto, method)
				if (rdr != "no") {	//server is listening and respone to a destination url
					output <- rdr
					if preferHTTPS {
						continue
					}
				}
				httpURLs <- url
			}
			httpsWG.Done()
		}()
	}

	// HTTP workers
	var httpWG sync.WaitGroup
	for i := 0; i < concurrency/2; i++ {
		httpWG.Add(1)

		go func() {
			for url := range httpURLs {
				withProto := "http://" + url
				rdr := isListening(client, withProto, method)
				//if isListening(client, withProto, method) {
				if (rdr != "no") {
					output <- rdr
					continue
				}
			}

			httpWG.Done()
		}()
	}

	// Close the httpURLs channel when the HTTPS workers are done
	go func() {
		httpsWG.Wait()
		close(httpURLs)
	}()

	// Output worker
	var outputWG sync.WaitGroup
	outputWG.Add(1)
	go func() {
		for o := range output {
			fmt.Println(o)
		}
		outputWG.Done()
	}()

	// Close the output channel when the HTTP workers are done
	go func() {
		httpWG.Wait()
		close(output)
	}()

	// accept domains on stdin
	sc := bufio.NewScanner(os.Stdin)
	for sc.Scan() {
		domain := strings.ToLower(sc.Text())

		// submit standard port checks
		if !skipDefault {
			httpsURLs <- domain
		}

		// Adding port templates
		xlarge := []string{"81", "300", "591", "593", "832", "981", "1010", "1311", "2082", "2087", "2095", "2096", "2480", "3000", "3128", "3333", "4243", "4567", "4711", "4712", "4993", "5000", "5104", "5108", "5800", "6543", "7000", "7001", "7777", "7396", "7474", "8000", "8001", "8008", "8014", "8042", "8069", "8080", "8081", "8082", "8088", "8090", "8091", "8118", "8123", "8172", "8222", "8243", "8280", "8281", "8333", "8443", "8500", "8834", "8880", "8888", "8983", "9000", "9043", "9060", "9080", "9090", "9091", "9200", "9443", "9800", "9981", "12443", "16080", "18091", "18092", "20720", "28017"}
		large := []string{"81", "591", "2082", "2087", "2095", "2096", "3000", "7001", "7777", "8000", "8001", "8008", "8080", "8083", "8082", "8443", "8834", "8888"}

		// submit any additional proto:port probes
		for _, p := range probes {
			switch p {
			case "xlarge":
				for _, port := range xlarge {
					httpsURLs <- fmt.Sprintf("%s:%s", domain, port)
				}
			case "large":
				for _, port := range large {
					httpsURLs <- fmt.Sprintf("%s:%s", domain, port)
				}
			default:
				pair := strings.SplitN(p, ":", 2)
				if len(pair) != 2 {
					continue
				}

				// This is a little bit funny as "https" will imply an
				// http check as well unless the --prefer-https flag is
				// set. On balance I don't think that's *such* a bad thing
				// but it is maybe a little unexpected.
				if strings.ToLower(pair[0]) == "https" {
					httpsURLs <- fmt.Sprintf("%s:%s", domain, pair[1])
				} else {
					httpURLs <- fmt.Sprintf("%s:%s", domain, pair[1])
				}
			}
		}
	}

	// once we've sent all the URLs off we can close the
	// input/httpsURLs channel. The workers will finish what they're
	// doing and then call 'Done' on the WaitGroup
	close(httpsURLs)

	// check there were no errors reading stdin (unlikely)
	if err := sc.Err(); err != nil {
		fmt.Fprintf(os.Stderr, "failed to read input: %s\n", err)
	}

	// Wait until the output waitgroup is done
	outputWG.Wait()
}

func isListening1(client *http.Client, url, method string) string {
	//quantt - add to check redirect
	//client.CheckRedirect = func(req1 *http.Request, via []*http.Request) errors {
    //    return errors.New("Redirect")
    //}
	//
	
	req, err := http.NewRequest(method, url, nil)
	if err != nil {
		return "no"
	}

	req.Header.Add("Connection", "close")
	req.Close = true
	
	//Submit request
	resp, err := client.Do(req)

	finalURL := resp.Request.URL.String()
    fmt.Println("Procesing URL "+url+ " and the URL you ended up at is: "+finalURL)
	
	if resp != nil {	//successful
		if resp.StatusCode == http.StatusFound || resp.StatusCode == http.StatusMovedPermanently { //status code 302 || 301
			io.Copy(ioutil.Discard, resp.Body)
			resp.Body.Close()
			return resp.Header.Get("Location")
			
			//if strings.Index(resp.Header.Get("Location"),url)==-1 {//Redirect location not contain url
			//	return resp.Header.Get("Location")
			//} else {	//return url similar to http:// https:// url/newlocation...
				//return("/"+resp.Header.Get("Location"))	//return redirected location
				//fmt.Println("Thay redirect: "+resp.Header.Get("Location"))
				//fmt.Println("URL: "+url)
				//fmt.Println("Trimleft redirect: "+strings.TrimLeft(resp.Header.Get("Location"), url))
				//fmt.Println("TrimRight redirect: "+strings.TrimRight(resp.Header.Get("Location"), url))
			//	return "/"+strings.TrimLeft(resp.Header.Get("Location"), url)
			//}
        } else {
			io.Copy(ioutil.Discard, resp.Body)
			resp.Body.Close()
        }		
	}

	if err != nil { //failed
		return "no"	//do not listening on this port
	}

	return ""	//no redirect
}

func isListening2 (client *http.Client, url, method string) string { 
	resp, err := http.Get(url)
	//fmt.Println("Procesing "+url)
	if err != nil {
		//fmt.Println("Procesing "+url+" ERROR")
        return "no"
    }
	defer resp.Body.Close()
	//fmt.Println("Procesing "+url+" Result: "+resp.Request.URL.String())
	return resp.Request.URL.String()
}

func isListening (client *http.Client, url, method string) string { 
	client1 := http.Client{
        Timeout: time.Duration(3000 * time.Millisecond),
    }
	resp, err := client1.Get(url)
	fmt.Println("Procesing "+url)
	if err != nil {
		fmt.Println("Procesing "+url+" ERROR")
        return "no"
    }
	defer resp.Body.Close()
	fmt.Println("Procesing "+url+" Result: "+resp.Request.URL.String())
	return resp.Request.URL.String()
}