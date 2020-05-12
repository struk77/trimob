package main

import (
	"crypto/tls"
	"database/sql"
	"flag"
	"fmt"
	_ "github.com/go-sql-driver/mysql"
	"golang.org/x/net/html"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

type configuration struct {
	dbName       string
	dbUser       string
	dbPassword   string
	listTable    string
	balanceTable string
}

type status struct {
	balance float64
	isFresh bool
}

type site struct {
	id         int
	name       string
	number     string
	password   string
	siteStatus status
}

func getSites(conf configuration) (result []site) {
	connectString := conf.dbUser + ":" + conf.dbPassword + "@/" + conf.dbName
	db, err := sql.Open("mysql", connectString)
	if err != nil {
		panic(err)
	}
	defer db.Close()
	rows, err := db.Query("SELECT * from " + conf.listTable)
	if err != nil {
		panic(err)
	}
	for rows.Next() {
		s := site{}
		err := rows.Scan(&s.id, &s.name, &s.number, &s.password)
		if err != nil {
			fmt.Println(err)
			continue
		}
		result = append(result, s)
	}
	return
}

func writeDb(checkingSite site, conf configuration) error {
	t := time.Now()
	t.Format("2006-01-02")
	connectString := conf.dbUser + ":" + conf.dbPassword + "@/" + conf.dbName
	db, err := sql.Open("mysql", connectString)
	if err != nil {
		panic(err)
	}
	defer db.Close()
	stmt, err := db.Prepare("INSERT INTO " + conf.balanceTable + "(site_id, check_date, balance) VALUES(?, ?, ?)")
	if err != nil {
		return err
	}
	defer stmt.Close()
	_, err = stmt.Exec(checkingSite.id, t, checkingSite.siteStatus.balance)
	if err != nil {
		//http.Error(w, http.StatusText(500), 500)
		return err
	}
	return nil
}

func getStatus(checkingSite site, ch chan site, conf configuration) {
	client := &http.Client{
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		},
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{
				InsecureSkipVerify: true,
			},
		},
	}
	form := url.Values{}
	form.Add("phone", checkingSite.number)
	form.Add("password", checkingSite.password)
	req, err := http.NewRequest("POST", "https://my.3mob.ua/ua/login", strings.NewReader(form.Encode()))
	if err != nil {
		panic(err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded; charset=UTF-8")
	req.Header.Add("Accept", "application/json, text/javascript, */*; q=0.01")
	req.Header.Add("X-Requested-With", "XMLHttpRequest")
	req.Header.Add("User-Agent", "Mozilla/5.0 (Windows NT 6.1; W...) Gecko/20100101 Firefox/64.0")
	resp, err := client.Do(req)
	if err != nil {
		panic(err)
	}
	defer resp.Body.Close()

	req, err = http.NewRequest("GET", "https://my.3mob.ua/ua/finance/balance", nil)
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 6.1; W...) Gecko/20100101 Firefox/64.0")
	req.Header.Add("Cookie", "dancer.session="+resp.Cookies()[0].Value)
	resp2, err := client.Do(req)
	if err != nil {
		panic(err)
	}
	defer resp2.Body.Close()
	z := html.NewTokenizer(resp2.Body)
	for {
		tt := z.Next()
		switch {
		case tt == html.ErrorToken:
			// End of the document, we're done
			return
		case tt == html.StartTagToken:
			t := z.Token()
			isTd := t.Data == "td" && len(t.Attr) > 0
			if isTd {
				if t.Attr[0].Val == "bold-and-beaty" {
					z.Next()
					balStr := z.Token().Data
					balance, err := strconv.ParseFloat(balStr, 64)
					if err != nil {
						balance = -100.0
					}
					z.Next()
					z.Next()
					z.Next()
					dDate := strings.Split(z.Token().Data, ".")
					d, err := strconv.Atoi(dDate[0])
					m, err := strconv.Atoi(dDate[1])
					y, err := strconv.Atoi(strings.Split(dDate[2], " ")[0])
					if err != nil {
						fmt.Println(err)
					}
					t := time.Date(y, time.Month(m), d, 0, 0, 0, 0, time.UTC)
					isFresh := t.Sub(time.Now()) > 2.628e+15
					checkingSite.siteStatus = status{balance, isFresh}
					err = writeDb(checkingSite, conf)
					if err != nil {
						fmt.Println(err)
					}
					ch <- checkingSite
					return
				}
			}
		}
	}
}

func sendAlarm(badSite site, token, chat string, wg *sync.WaitGroup) {
	defer wg.Done()
	apiURL := "https://api.telegram.org/bot" + token + "/sendMessage"
	text := "Все номера тримоб чувствуют себя великолепно!"
	if badSite.name != "" {
		if badSite.siteStatus.balance > -100.0 {
			if badSite.siteStatus.isFresh {
				text = "Low balance (" + fmt.Sprintf("%.2f", badSite.siteStatus.balance) + " UAH) on " + badSite.name + ". Please charge the number " + badSite.number
			} else {
				text = "Please charge the number " + badSite.number + " on " + badSite.name + "\nLess then one month to deactivation!"
			}
		} else {
			text = "Parsing error on number " + badSite.number
		}
	}
	client := &http.Client{}
	form := url.Values{}
	form.Add("chat_id", chat)
	form.Add("text", text)
	req, err := http.NewRequest("POST", apiURL, strings.NewReader(form.Encode()))
	if err != nil {
		fmt.Println(err)
		return
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded; charset=UTF-8")
	req.Header.Add("Accept", "application/json, text/javascript, */*; q=0.01")
	req.Header.Add("X-Requested-With", "XMLHttpRequest")
	resp, err := client.Do(req)
	if err != nil {
		fmt.Println(err)
		return
	}
	defer resp.Body.Close()
	fmt.Printf("Sending alarm status of %s: %s\n", badSite.name, resp.Status)
	return
}

func main() {
	fmt.Println("main() started")
	var wg sync.WaitGroup
	isOk := true
	dbNamePtr := flag.String("d", "database", "database name")
	dbUserPtr := flag.String("u", "user", "database user name")
	dbPasswordPtr := flag.String("p", "password", "database password")
	listTablePtr := flag.String("l", "sites", "list of sites")
	balanceTablePtr := flag.String("b", "balances", "historical log of balances")
	tgBotAPIPtr := flag.String("t", "222:333", "Telegram bot token")
	tgChatIDPtr := flag.String("c", "222333", "Telegram chat id")
	flag.Parse()

	conf := configuration{*dbNamePtr, *dbUserPtr, *dbPasswordPtr, *listTablePtr, *balanceTablePtr}
	sites := getSites(conf)
	chSites := make(chan site, len(sites))

	for _, s := range sites {
		go getStatus(s, chSites, conf)
	}
	for i := 0; i < len(sites); i++ {
		val, ok := <-chSites
		if !ok {
			break
		}
		fmt.Println(val)
		if val.siteStatus.balance < 15 || !val.siteStatus.isFresh {
			fmt.Println("Alarmed ", val.name)
			wg.Add(1)
			go sendAlarm(val, *tgBotAPIPtr, *tgChatIDPtr, &wg)
			isOk = false
		}
	}
	close(chSites)

	if isOk {
		wg.Add(1)
		go sendAlarm(site{}, *tgBotAPIPtr, *tgChatIDPtr, &wg)
	}
	fmt.Println("Waiting for alarms")
	wg.Wait()
	fmt.Println("main() completed")

}
