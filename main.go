package main

import (
	"bufio"
	"bytes"
	"encoding/gob"
	"flag"
	"fmt"
	. "github.com/iotaledger/iota.go/api"
	"github.com/iotaledger/iota.go/bundle"
	"github.com/iotaledger/iota.go/pow"
	"github.com/iotaledger/iota.go/transaction"
	. "github.com/iotaledger/iota.go/trinary"
	"io/ioutil"
	"math/rand"
	"os"
	"strings"
	"time"
)

var emptySeed = strings.Repeat("9", 81)

const defaultNode = "https://trinity.iota-tangle.io:14265"
const defaultTag = "SUBGEN"
const defaultTxsCount = 50

// flags
var num = flag.Int("txs", defaultTxsCount, "number of txs of the subtangle")
var node = flag.String("node", defaultNode, "the node to use")
var tag = flag.String("tag", defaultTag, "the tag to use")
var remotePoW = flag.Bool("remote", true, "whether to do remote PoW")
var broadcastInterval = flag.Int("broadcastInterval", 10, "the interval (ms) between sending off txs of the build subtangle")
var retain = flag.Bool("retain", false, "whether to indefinitely generate txs and broadcast them up on key press")
var wideness = flag.Int("wideness", 30, "wideness of the subtangle")

const snapshotFile = "./subtangle.snap"

func must(err error) {
	if err != nil {
		panic(err)
	}
}

type Subtangle = transaction.Transactions

func main() {
	flag.Parse()

	gob.Register(Subtangle{})

	settings := HTTPClientSettings{URI: *node}
	_, powFunc := pow.GetFastestProofOfWorkImpl()
	if !*remotePoW {
		settings.LocalProofOfWorkFunc = powFunc
	}
	api, err := ComposeAPI(settings)
	must(err)

	existing := readPersisted()
	if existing != nil {
		broadcast(existing, api)
		return
	}

	subtangle := build(api)
	broadcast(subtangle, api)
}

func readPersisted() Subtangle {
	_, err := os.Stat(snapshotFile)
	switch {
	case os.IsNotExist(err):
		return nil
	default:
		must(err)
	}
	binSubtangle, err := ioutil.ReadFile(snapshotFile)
	must(err)

	subtangle := Subtangle{}
	dec := gob.NewDecoder(bytes.NewReader(binSubtangle))
	must(dec.Decode(&subtangle))
	return subtangle
}

func persist(subtangle Subtangle) {
	os.Remove(snapshotFile)
	var buf bytes.Buffer
	enc := gob.NewEncoder(&buf)
	must(enc.Encode(subtangle))
	if err := ioutil.WriteFile(snapshotFile, buf.Bytes(), 0755); err != nil {
		fmt.Println("unable to write snapshot file:", err.Error())
	}
}

func build(api *API) Subtangle {
	initialTips, err := api.GetTransactionsToApprove(3)
	must(err)

	emptyTransfers := bundle.Transfers{bundle.EmptyTransfer}
	emptyTransfers[0].Tag = *tag
	subtangleSize := *num
	var stopGenerating chan struct{}
	if *retain {
		stopGenerating = make(chan struct{})
		fmt.Printf(">retain mode, generating txs indefinitely (hit enter to broadcast)\n")
		subtangleSize = 1000000
		go func() {
			reader := bufio.NewReader(os.Stdin)
			reader.ReadLine()
			stopGenerating <- struct{}{}
		}()
	}
	subtangle := Subtangle{}
	w := *wideness
out:
	for i := 0; i < subtangleSize; i++ {
		if stopGenerating != nil {
			select {
			case <-stopGenerating:
				break out
			default:
			}
		}
		prep, err := api.PrepareTransfers(emptySeed, emptyTransfers, PrepareTransfersOptions{})
		must(err)

		// first transaction which connects to the main tangle in the past
		var trunk, branch Hash
		if i == 0 {
			trunk = initialTips.TrunkTransaction
			branch = initialTips.BranchTransaction
		} else {
			// pick random transactions from the last N of our own txs
			rT := rand.Int()
			rB := rand.Int()
			l := len(subtangle)
			if l < w {
				trunk = subtangle[rT%len(subtangle)].Hash
				branch = subtangle[rB%len(subtangle)].Hash
			} else {
				trunk = subtangle[l-w+rT%w].Hash
				branch = subtangle[l-w+rB%w].Hash
			}
		}
		readyTrytes, err := api.AttachToTangle(trunk, branch, 14, prep)
		must(err)
		tx, err := transaction.AsTransactionObject(readyTrytes[0])
		must(err)
		subtangle = append(subtangle, *tx)
		if *retain {
			fmt.Printf("\rgenerating txs %d", i+1)
		} else {
			fmt.Printf("\rgenerating txs %d/%d", i+1, subtangleSize)
		}
	}

	// persist the built subtangle
	persist(subtangle)

	return subtangle
}

func broadcast(subtangle Subtangle, api *API) {
	defer os.Remove(snapshotFile)

	// add a tx which connect back to the main tangle
	prep, err := api.PrepareTransfers(emptySeed, bundle.Transfers{bundle.EmptyTransfer}, PrepareTransfersOptions{})
	must(err)

	tips, err := api.GetTransactionsToApprove(3)
	must(err)

	readyTrytes, err := api.AttachToTangle(tips.TrunkTransaction, subtangle[len(subtangle)-1].Hash, 14, prep)
	must(err)

	tx, err := transaction.AsTransactionObject(readyTrytes[0])
	must(err)
	subtangle = append(subtangle, *tx)

	txs := transaction.MustTransactionsToTrytes(subtangle)
	for i, tx := range txs {
		tries := 0
		for ; tries < 5; tries++ {
			if _, err := api.BroadcastTransactions(tx); err != nil {
				continue
			}
			break
		}
		fmt.Printf("\rbroadcasting txs %d/%d", i+1, len(txs))
		<-time.After(time.Duration(*broadcastInterval) * time.Millisecond)
	}
	fmt.Printf("\npublished %d txs to the Tangle\n", len(subtangle))
}
