package trade

import (
	"sync"
	"time"

	"github.com/gofrs/uuid"
	"github.com/thrasher-corp/gocryptotrader/currency"
	"github.com/thrasher-corp/gocryptotrader/exchanges/asset"
	"github.com/thrasher-corp/gocryptotrader/exchanges/order"
)

const DefaultProcessorIntervalTime = time.Second * 15

var (
	processor Processor
	// BufferProcessorIntervalTime is the interval to save trade buffer data to the database.
	// Change this by changing the runtime param `-tradeprocessinginterval=15s`
	BufferProcessorIntervalTime = DefaultProcessorIntervalTime
)

// Data defines trade data
type Data struct {
	ID           uuid.UUID `json:"ID,omitempty"`
	TID          string
	Exchange     string
	CurrencyPair currency.Pair
	AssetType    asset.Item
	Side         order.Side
	Price        float64
	Amount       float64
	Timestamp    time.Time
}

// Processor used for processing trade data in batches
// and saving them to the database
type Processor struct {
	mutex                   sync.Mutex
	started                 int32
	bufferProcessorInterval time.Duration
	buffer                  []Data
}

// ByDate sorts trades by date ascending
type ByDate []Data

func (b ByDate) Len() int {
	return len(b)
}

func (b ByDate) Less(i, j int) bool {
	return b[i].Timestamp.Before(b[j].Timestamp)
}

func (b ByDate) Swap(i, j int) {
	b[i], b[j] = b[j], b[i]
}
