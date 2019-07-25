package main

import (
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"runtime"
	"strconv"
	"syscall"

	"github.com/thrasher-/gocryptotrader/common"
	"github.com/thrasher-/gocryptotrader/communications"
	"github.com/thrasher-/gocryptotrader/config"
	"github.com/thrasher-/gocryptotrader/currency"
	"github.com/thrasher-/gocryptotrader/currency/coinmarketcap"
	exchange "github.com/thrasher-/gocryptotrader/exchanges"
	log "github.com/thrasher-/gocryptotrader/logger"
	"github.com/thrasher-/gocryptotrader/portfolio"
)

// Bot contains configuration, portfolio, exchange & ticker data and is the
// overarching type across this code base.
type Bot struct {
	config     *config.Config
	portfolio  *portfolio.Base
	exchanges  []exchange.IBotExchange
	comms      *communications.Communications
	shutdown   chan bool
	dryRun     bool
	configFile string
	dataDir    string
}

const banner = `
   ______        ______                     __        ______                  __
  / ____/____   / ____/_____ __  __ ____   / /_ ____ /_  __/_____ ______ ____/ /___   _____
 / / __ / __ \ / /    / ___// / / // __ \ / __// __ \ / /  / ___// __  // __  // _ \ / ___/
/ /_/ // /_/ // /___ / /   / /_/ // /_/ // /_ / /_/ // /  / /   / /_/ // /_/ //  __// /
\____/ \____/ \____//_/    \__, // .___/ \__/ \____//_/  /_/    \__,_/ \__,_/ \___//_/
                          /____//_/
`

var bot Bot

func main() {
	bot.shutdown = make(chan bool)
	HandleInterrupt()

	defaultPath, err := config.GetFilePath("")
	if err != nil {
		log.Fatal(err)
	}

	// Handle flags
	flag.StringVar(&bot.configFile, "config", defaultPath, "config file to load")
	flag.StringVar(&bot.dataDir, "datadir", common.GetDefaultDataDir(runtime.GOOS), "default data directory for GoCryptoTrader files")
	dryrun := flag.Bool("dryrun", false, "dry runs bot, doesn't save config file")
	version := flag.Bool("version", false, "retrieves current GoCryptoTrader version")
	verbosity := flag.Bool("verbose", false, "increases logging verbosity for GoCryptoTrader")

	Coinmarketcap := flag.Bool("c", false, "overrides config and runs currency analaysis")
	FxCurrencyConverter := flag.Bool("fxa", false, "overrides config and sets up foreign exchange Currency Converter")
	FxCurrencyLayer := flag.Bool("fxb", false, "overrides config and sets up foreign exchange Currency Layer")
	FxFixer := flag.Bool("fxc", false, "overrides config and sets up foreign exchange Fixer.io")
	FxOpenExchangeRates := flag.Bool("fxd", false, "overrides config and sets up foreign exchange Open Exchange Rates")

	flag.Parse()

	if *version {
		fmt.Print(BuildVersion(true))
		os.Exit(0)
	}

	if *dryrun {
		bot.dryRun = true
	}

	fmt.Println(banner)
	fmt.Println(BuildVersion(false))

	bot.config = &config.Cfg
	log.Debugf("Loading config file %s..\n", bot.configFile)
	err = bot.config.LoadConfig(bot.configFile)
	if err != nil {
		log.Fatalf("Failed to load config. Err: %s", err)
	}

	err = common.CheckDir(bot.dataDir, true)
	if err != nil {
		log.Fatalf("Failed to open/create data directory: %s. Err: %s", bot.dataDir, err)
	}
	log.Debugf("Using data directory: %s.\n", bot.dataDir)

	err = bot.config.CheckLoggerConfig()
	if err != nil {
		log.Errorf("Failed to configure logger reason: %s", err)
	}

	err = log.SetupLogger()
	if err != nil {
		log.Errorf("Failed to setup logger reason: %s", err)
	}

	AdjustGoMaxProcs()
	log.Debugf("Bot '%s' started.\n", bot.config.Name)
	log.Debugf("Bot dry run mode: %v.\n", common.IsEnabled(bot.dryRun))

	log.Debugf("Available Exchanges: %d. Enabled Exchanges: %d.\n",
		len(bot.config.Exchanges),
		bot.config.CountEnabledExchanges())

	common.HTTPClient = common.NewHTTPClientWithTimeout(bot.config.GlobalHTTPTimeout)
	log.Debugf("Global HTTP request timeout: %v.\n", common.HTTPClient.Timeout)

	SetupExchanges()
	if len(bot.exchanges) == 0 {
		log.Fatalf("No exchanges were able to be loaded. Exiting")
	}

	log.Debugf("Starting communication mediums..")
	cfg := bot.config.GetCommunicationsConfig()
	bot.comms = communications.NewComm(&cfg)
	bot.comms.GetEnabledCommunicationMediums()

	var newFxSettings []currency.FXSettings
	for _, d := range bot.config.Currency.ForexProviders {
		newFxSettings = append(newFxSettings, currency.FXSettings(d))
	}

	err = currency.RunStorageUpdater(currency.BotOverrides{
		Coinmarketcap:       *Coinmarketcap,
		FxCurrencyConverter: *FxCurrencyConverter,
		FxCurrencyLayer:     *FxCurrencyLayer,
		FxFixer:             *FxFixer,
		FxOpenExchangeRates: *FxOpenExchangeRates,
	},
		&currency.MainConfiguration{
			ForexProviders:         newFxSettings,
			CryptocurrencyProvider: coinmarketcap.Settings(bot.config.Currency.CryptocurrencyProvider),
			Cryptocurrencies:       bot.config.Currency.Cryptocurrencies,
			FiatDisplayCurrency:    bot.config.Currency.FiatDisplayCurrency,
			CurrencyDelay:          bot.config.Currency.CurrencyFileUpdateDuration,
			FxRateDelay:            bot.config.Currency.ForeignExchangeUpdateDuration,
		},
		bot.dataDir,
		*verbosity)
	if err != nil {
		log.Fatalf("currency updater system failed to start %v", err)

	}

	bot.portfolio = &portfolio.Portfolio
	bot.portfolio.SeedPortfolio(bot.config.Portfolio)
	SeedExchangeAccountInfo(GetAllEnabledExchangeAccountInfo().Data)

	if bot.config.Webserver.Enabled {
		listenAddr := bot.config.Webserver.ListenAddress
		log.Debugf(
			"HTTP Webserver support enabled. Listen URL: http://%s:%d/\n",
			common.ExtractHost(listenAddr), common.ExtractPort(listenAddr),
		)

		router := NewRouter()
		go func() {
			err = http.ListenAndServe(listenAddr, router)
			if err != nil {
				log.Fatal(err)
			}
		}()

		log.Debugln("HTTP Webserver started successfully.")
		log.Debugln("Starting websocket handler.")
		StartWebsocketHandler()
	} else {
		log.Debugln("HTTP RESTful Webserver support disabled.")
	}

	go portfolio.StartPortfolioWatcher()

	// go TickerUpdaterRoutine()
	// go OrderbookUpdaterRoutine()
	// go WebsocketRoutine(*verbosity)

	// var h exchange.IBotExchange
	// for i := range bot.exchanges{
	// 	if bot.exchanges[i].GetName()== "Huobi"{
	// 		h = bot.exchanges[i]
	// 		log.Debugln("the h is..",h)
	// 	}
	// }

	testh := GetExchangeByName("Huobi")
	// println("....h....",testh)
	println("....testh....",testh)
	// h.GetTickerPrice
	assettype,err := exchange.GetExchangeAssetTypes("Huobi")
	if err != nil {
		log.Debugln("what's the error ",err)
	}
	log.Debugln("the assettype is:",assettype)

	//初始化交易对
	btcpair :=  currency.NewPairFromString("BTC-USDT")
	ticker, err := testh.GetTickerPrice(btcpair,assettype[0])
	if err != nil {
		log.Debugln("try to use huobi for learning...:",err)
	}
	log.Debugf("got the ticker ?...%+v",ticker,"\n")


	//获得account的info
	clientinfo,err := testh.GetAccountInfo()
	if err != nil{
		log.Debugln("fail to get the account info from huobi website....",err)
	}
	log.Debugf("the account info is ....%+v",clientinfo.Accounts[0].ID,"\n")
	//初始化币
	var p = currency.Pair{
		Delimiter: "",
		Base:      currency.HT,
		Quote:     currency.USDT,
	}

	subres,err := testh.SubmitOrder(p,exchange.SellOrderSide,exchange.MarketOrderType,1,2.5,clientinfo.Accounts[0].ID)
	if err != nil{
		log.Debugln("sub error.....",err)
	}
	log.Debugf("sub result...%+v",subres,"\n")

	book,err := testh.UpdateOrderbook(p,assettype[0]);
	if err != nil {
		log.Errorf("Fail to update the book.....",err)
	}
	log.Debugf("the book is.....%+v",book,"\n")

	//获取历史数据，用于分析曲线
	history,err := testh.GetExchangeHistory(btcpair,assettype[0])
	if err != nil {
		log.Debugf("what's the err:",err)
	}else{
		log.Debugln("the result is...",history)
	}

	hbws,_ := testh.GetWebsocket()
	
	// h.GetSpotKline(exchange.huobi.KlinesRequestParams)
	go WebsocketDataHandler(hbws, true)
	err = hbws.Connect()
	if err != nil {
		log.Debugln("the error....",err )
	}
	log.Debugln(hbws.Orderbook)

	
	<-bot.shutdown
	Shutdown()
}

// AdjustGoMaxProcs adjusts the maximum processes that the CPU can handle.
func AdjustGoMaxProcs() {
	log.Debugln("Adjusting bot runtime performance..")
	maxProcsEnv := os.Getenv("GOMAXPROCS")
	maxProcs := runtime.NumCPU()
	log.Debugln("Number of CPU's detected:", maxProcs)

	if maxProcsEnv != "" {
		log.Debugln("GOMAXPROCS env =", maxProcsEnv)
		env, err := strconv.Atoi(maxProcsEnv)
		if err != nil {
			log.Debugf("Unable to convert GOMAXPROCS to int, using %d", maxProcs)
		} else {
			maxProcs = env
		}
	}
	if i := runtime.GOMAXPROCS(maxProcs); i != maxProcs {
		log.Error("Go Max Procs were not set correctly.")
	}
	log.Debugln("Set GOMAXPROCS to:", maxProcs)
}

// HandleInterrupt monitors and captures the SIGTERM in a new goroutine then
// shuts down bot
func HandleInterrupt() {
	c := make(chan os.Signal, 1)
	signal.Notify(c, os.Interrupt, syscall.SIGTERM)
	go func() {
		sig := <-c
		log.Debugf("Captured %v, shutdown requested.", sig)
		bot.shutdown <- true
	}()
}

// Shutdown correctly shuts down bot saving configuration files
func Shutdown() {
	log.Debugln("Bot shutting down..")

	if len(portfolio.Portfolio.Addresses) != 0 {
		bot.config.Portfolio = portfolio.Portfolio
	}

	if !bot.dryRun {
		err := bot.config.SaveConfig(bot.configFile)

		if err != nil {
			log.Warn("Unable to save config.")
		} else {
			log.Debugln("Config file saved successfully.")
		}
	}

	log.Debugln("Exiting.")

	log.CloseLogFile()
	os.Exit(0)
}
