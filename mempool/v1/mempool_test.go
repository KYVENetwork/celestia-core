package v1

import (
	"bytes"
	"errors"
	"fmt"
	"math/rand"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/KYVENetwork/celestia-core/abci/example/code"
	"github.com/KYVENetwork/celestia-core/abci/example/kvstore"
	abci "github.com/KYVENetwork/celestia-core/abci/types"
	"github.com/KYVENetwork/celestia-core/config"
	"github.com/KYVENetwork/celestia-core/libs/log"
	"github.com/KYVENetwork/celestia-core/mempool"
	"github.com/KYVENetwork/celestia-core/pkg/consts"
	tmproto "github.com/KYVENetwork/celestia-core/proto/celestiacore/types"
	"github.com/KYVENetwork/celestia-core/proxy"
	"github.com/KYVENetwork/celestia-core/types"
)

// application extends the KV store application by overriding CheckTx to provide
// transaction priority based on the value in the key/value pair.
type application struct {
	*kvstore.Application
}

type testTx struct {
	tx       types.Tx
	priority int64
}

func (app *application) CheckTx(req abci.RequestCheckTx) abci.ResponseCheckTx {
	var (
		priority int64
		sender   string
	)

	// infer the priority from the raw transaction value (sender=key=value)
	parts := bytes.Split(req.Tx, []byte("="))
	if len(parts) == 3 {
		v, err := strconv.ParseInt(string(parts[2]), 10, 64)
		if err != nil {
			return abci.ResponseCheckTx{
				Priority:  priority,
				Code:      100,
				GasWanted: 1,
			}
		}

		priority = v
		sender = string(parts[0])
	} else {
		return abci.ResponseCheckTx{
			Priority:  priority,
			Code:      101,
			GasWanted: 1,
		}
	}

	return abci.ResponseCheckTx{
		Priority:  priority,
		Sender:    sender,
		Code:      code.CodeTypeOK,
		GasWanted: 1,
	}
}

func setup(t testing.TB, cacheSize int, options ...TxMempoolOption) *TxMempool {
	t.Helper()

	app := &application{kvstore.NewApplication()}
	cc := proxy.NewLocalClientCreator(app)

	cfg := config.ResetTestRoot(strings.ReplaceAll(t.Name(), "/", "|"))
	cfg.Mempool.CacheSize = cacheSize

	appConnMem, err := cc.NewABCIClient()
	require.NoError(t, err)
	require.NoError(t, appConnMem.Start())

	t.Cleanup(func() {
		os.RemoveAll(cfg.RootDir)
		require.NoError(t, appConnMem.Stop())
	})

	return NewTxMempool(log.TestingLogger().With("test", t.Name()), cfg.Mempool, appConnMem, 0, options...)
}

// mustCheckTx invokes txmp.CheckTx for the given transaction and waits until
// its callback has finished executing. It fails t if CheckTx fails.
func mustCheckTx(t *testing.T, txmp *TxMempool, spec string) {
	done := make(chan struct{})
	if err := txmp.CheckTx([]byte(spec), func(*abci.Response) {
		close(done)
	}, mempool.TxInfo{}); err != nil {
		t.Fatalf("CheckTx for %q failed: %v", spec, err)
	}
	<-done
}

// checkTxs generates a specified number of txs, checks them into the mempool,
// and returns them.
func checkTxs(t *testing.T, txmp *TxMempool, numTxs int, peerID uint16) []testTx {
	txs := make([]testTx, numTxs)
	txInfo := mempool.TxInfo{SenderID: peerID}

	rng := rand.New(rand.NewSource(time.Now().UnixNano()))

	for i := 0; i < numTxs; i++ {
		prefix := make([]byte, 20)
		_, err := rng.Read(prefix)
		require.NoError(t, err)

		priority := int64(rng.Intn(9999-1000) + 1000)

		txs[i] = testTx{
			tx:       []byte(fmt.Sprintf("sender-%03d-%d=%X=%d", i, peerID, prefix, priority)),
			priority: priority,
		}
		require.NoError(t, txmp.CheckTx(txs[i].tx, nil, txInfo))
	}

	return txs
}

func TestTxMempool_TxsAvailable(t *testing.T) {
	txmp := setup(t, 0)
	txmp.EnableTxsAvailable()

	ensureNoTxFire := func() {
		timer := time.NewTimer(500 * time.Millisecond)
		select {
		case <-txmp.TxsAvailable():
			require.Fail(t, "unexpected transactions event")
		case <-timer.C:
		}
	}

	ensureTxFire := func() {
		timer := time.NewTimer(500 * time.Millisecond)
		select {
		case <-txmp.TxsAvailable():
		case <-timer.C:
			require.Fail(t, "expected transactions event")
		}
	}

	// ensure no event as we have not executed any transactions yet
	ensureNoTxFire()

	// Execute CheckTx for some transactions and ensure TxsAvailable only fires
	// once.
	txs := checkTxs(t, txmp, 100, 0)
	ensureTxFire()
	ensureNoTxFire()

	rawTxs := make([]types.Tx, len(txs))
	for i, tx := range txs {
		rawTxs[i] = tx.tx
	}

	responses := make([]*abci.ResponseDeliverTx, len(rawTxs[:50]))
	for i := 0; i < len(responses); i++ {
		responses[i] = &abci.ResponseDeliverTx{Code: abci.CodeTypeOK}
	}

	// commit half the transactions and ensure we fire an event
	txmp.Lock()
	require.NoError(t, txmp.Update(1, rawTxs[:50], responses, nil, nil))
	txmp.Unlock()
	ensureTxFire()
	ensureNoTxFire()

	// Execute CheckTx for more transactions and ensure we do not fire another
	// event as we're still on the same height (1).
	_ = checkTxs(t, txmp, 100, 0)
	ensureNoTxFire()
}

func TestTxMempool_Size(t *testing.T) {
	txmp := setup(t, 0)
	txs := checkTxs(t, txmp, 100, 0)
	require.Equal(t, len(txs), txmp.Size())
	require.Equal(t, int64(5800), txmp.SizeBytes())

	rawTxs := make([]types.Tx, len(txs))
	for i, tx := range txs {
		rawTxs[i] = tx.tx
	}

	responses := make([]*abci.ResponseDeliverTx, len(rawTxs[:50]))
	for i := 0; i < len(responses); i++ {
		responses[i] = &abci.ResponseDeliverTx{Code: abci.CodeTypeOK}
	}

	txmp.Lock()
	require.NoError(t, txmp.Update(1, rawTxs[:50], responses, nil, nil))
	txmp.Unlock()

	require.Equal(t, len(rawTxs)/2, txmp.Size())
	require.Equal(t, int64(2900), txmp.SizeBytes())
}

func TestTxMempool_Eviction(t *testing.T) {
	txmp := setup(t, 1000)
	txmp.config.Size = 5
	txmp.config.MaxTxsBytes = 60
	txExists := func(spec string) bool {
		txmp.Lock()
		defer txmp.Unlock()
		key := types.Tx(spec).Key()
		_, ok := txmp.txByKey[key]
		return ok
	}

	// A transaction bigger than the mempool should be rejected even when there
	// are slots available.
	mustCheckTx(t, txmp, "big=0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef=1")
	require.Equal(t, 0, txmp.Size())

	// Nearly-fill the mempool with a low-priority transaction, to show that it
	// is evicted even when slots are available for a higher-priority tx.
	const bigTx = "big=0123456789abcdef0123456789abcdef0123456789abcdef01234=2"
	mustCheckTx(t, txmp, bigTx)
	require.Equal(t, 1, txmp.Size()) // bigTx is the only element
	require.True(t, txExists(bigTx))
	require.Equal(t, int64(len(bigTx)), txmp.SizeBytes())

	// The next transaction should evict bigTx, because it is higher priority
	// but does not fit on size.
	mustCheckTx(t, txmp, "key1=0000=25")
	require.True(t, txExists("key1=0000=25"))
	require.False(t, txExists(bigTx))
	bigTxKey := types.Tx((bigTx)).Key()
	require.False(t, txmp.cache.HasKey(bigTxKey))
	require.True(t, txmp.WasRecentlyEvicted(bigTxKey)) // bigTx evicted
	require.Equal(t, int64(len("key1=0000=25")), txmp.SizeBytes())

	// Now fill up the rest of the slots with other transactions.
	mustCheckTx(t, txmp, "key2=0001=5")
	mustCheckTx(t, txmp, "key3=0002=10")
	mustCheckTx(t, txmp, "key4=0003=3")
	mustCheckTx(t, txmp, "key5=0004=3")

	// A new transaction with low priority should be discarded.
	mustCheckTx(t, txmp, "key6=0005=1")
	require.False(t, txExists("key6=0005=1"))
	require.True(t, txmp.WasRecentlyEvicted(types.Tx(("key6=0005=1")).Key())) // key6 evicted

	// A new transaction with higher priority should evict key5, which is the
	// newest of the two transactions with lowest priority.
	mustCheckTx(t, txmp, "key7=0006=7")
	require.True(t, txExists("key7=0006=7"))                                  // new transaction added
	require.False(t, txExists("key5=0004=3"))                                 // newest low-priority tx evicted
	require.True(t, txmp.WasRecentlyEvicted(types.Tx(("key5=0004=3")).Key())) // key5 evicted
	require.True(t, txExists("key4=0003=3"))                                  // older low-priority tx retained

	// Another new transaction evicts the other low-priority element.
	mustCheckTx(t, txmp, "key8=0007=20")
	require.True(t, txExists("key8=0007=20"))
	require.False(t, txExists("key4=0003=3"))

	// Now the lowest-priority tx is 5, so that should be the next to go.
	mustCheckTx(t, txmp, "key9=0008=9")
	require.True(t, txExists("key9=0008=9"))
	require.False(t, txExists("key2=0001=5"))
	require.True(t, txmp.WasRecentlyEvicted(types.Tx(("key2=0001=5")).Key())) // key2 evicted

	// Add a transaction that requires eviction of multiple lower-priority
	// entries, in order to fit the size of the element.
	mustCheckTx(t, txmp, "key10=0123456789abcdef=11") // evict 10, 9, 7; keep 25, 20, 11
	require.True(t, txExists("key1=0000=25"))
	require.True(t, txExists("key8=0007=20"))
	require.True(t, txExists("key10=0123456789abcdef=11"))
	require.False(t, txExists("key3=0002=10"))
	require.True(t, txmp.WasRecentlyEvicted(types.Tx(("key3=0002=10")).Key())) // key3 evicted
	require.False(t, txExists("key9=0008=9"))
	require.True(t, txmp.WasRecentlyEvicted(types.Tx(("key9=0008=9")).Key())) // key9 evicted
	require.False(t, txExists("key7=0006=7"))
	require.True(t, txmp.WasRecentlyEvicted(types.Tx(("key7=0006=7")).Key())) // key7 evicted
}

func TestTxMempool_Flush(t *testing.T) {
	txmp := setup(t, 0)
	txs := checkTxs(t, txmp, 100, 0)
	require.Equal(t, len(txs), txmp.Size())
	require.Equal(t, int64(5800), txmp.SizeBytes())

	rawTxs := make([]types.Tx, len(txs))
	for i, tx := range txs {
		rawTxs[i] = tx.tx
	}

	responses := make([]*abci.ResponseDeliverTx, len(rawTxs[:50]))
	for i := 0; i < len(responses); i++ {
		responses[i] = &abci.ResponseDeliverTx{Code: abci.CodeTypeOK}
	}

	txmp.Lock()
	require.NoError(t, txmp.Update(1, rawTxs[:50], responses, nil, nil))
	txmp.Unlock()

	txmp.Flush()
	require.Zero(t, txmp.Size())
	require.Equal(t, int64(0), txmp.SizeBytes())
}

func TestTxMempool_ReapMaxBytesMaxGas(t *testing.T) {
	// totalSizeBytes is the expected size of the mempool after adding 100 txs
	// this value is highly dependant upon the size of the txs and the overhead
	// introduced in the mempool. This number will need to be adjusted if
	// changes are made to any of those things.
	totalSizeBytes := int64(5800)
	txmp := setup(t, 0)
	tTxs := checkTxs(t, txmp, 100, 0) // all txs request 1 gas unit
	require.Equal(t, len(tTxs), txmp.Size())
	require.Equal(t, totalSizeBytes, txmp.SizeBytes())
	txMap := make(map[types.TxKey]testTx)
	priorities := make([]int64, len(tTxs))
	for i, tTx := range tTxs {
		txMap[tTx.tx.Key()] = tTx
		priorities[i] = tTx.priority
	}

	sort.Slice(priorities, func(i, j int) bool {
		// sort by priority, i.e. decreasing order
		return priorities[i] > priorities[j]
	})

	ensurePrioritized := func(reapedTxs types.Txs) {
		reapedPriorities := make([]int64, len(reapedTxs))
		for i, rTx := range reapedTxs {
			reapedPriorities[i] = txMap[rTx.Key()].priority
		}

		require.Equal(t, priorities[:len(reapedPriorities)], reapedPriorities)
	}

	// reap by gas capacity only
	reapedTxs := txmp.ReapMaxBytesMaxGas(-1, 50)
	ensurePrioritized(reapedTxs)
	require.Equal(t, len(tTxs), txmp.Size())
	require.Equal(t, totalSizeBytes, txmp.SizeBytes())
	require.Len(t, reapedTxs, 50)

	// reap by transaction bytes only
	reapedTxs = txmp.ReapMaxBytesMaxGas(1000, -1)
	ensurePrioritized(reapedTxs)
	require.Equal(t, len(tTxs), txmp.Size())
	require.Equal(t, totalSizeBytes, txmp.SizeBytes())
	require.GreaterOrEqual(t, len(reapedTxs), 16)

	// Reap by both transaction bytes and gas, where the size yields 31 reaped
	// transactions and the gas limit reaps 25 transactions.
	reapedTxs = txmp.ReapMaxBytesMaxGas(1500, 30)
	ensurePrioritized(reapedTxs)
	require.Equal(t, len(tTxs), txmp.Size())
	require.Equal(t, totalSizeBytes, txmp.SizeBytes())
	require.Len(t, reapedTxs, 25)
}

func TestTxMempoolTxLargerThanMaxBytes(t *testing.T) {
	rng := rand.New(rand.NewSource(time.Now().UnixNano()))
	txmp := setup(t, 0)
	bigPrefix := make([]byte, 100)
	_, err := rng.Read(bigPrefix)
	require.NoError(t, err)
	// large high priority tx
	bigTx := []byte(fmt.Sprintf("sender-1-1=%X=2", bigPrefix))
	smallPrefix := make([]byte, 20)
	_, err = rng.Read(smallPrefix)
	require.NoError(t, err)
	// smaller low priority tx with different sender
	smallTx := []byte(fmt.Sprintf("sender-2-1=%X=1", smallPrefix))
	require.NoError(t, txmp.CheckTx(bigTx, nil, mempool.TxInfo{SenderID: 1}))
	require.NoError(t, txmp.CheckTx(smallTx, nil, mempool.TxInfo{SenderID: 1}))

	// reap by max bytes less than the large tx
	reapedTxs := txmp.ReapMaxBytesMaxGas(100, -1)
	require.Len(t, reapedTxs, 1)
	require.Equal(t, types.Tx(smallTx), reapedTxs[0])
}

func TestTxMempool_ReapMaxTxs(t *testing.T) {
	txmp := setup(t, 0)
	tTxs := checkTxs(t, txmp, 100, 0)
	require.Equal(t, len(tTxs), txmp.Size())
	require.Equal(t, int64(5800), txmp.SizeBytes())

	txMap := make(map[types.TxKey]testTx)
	priorities := make([]int64, len(tTxs))
	for i, tTx := range tTxs {
		txMap[tTx.tx.Key()] = tTx
		priorities[i] = tTx.priority
	}

	sort.Slice(priorities, func(i, j int) bool {
		// sort by priority, i.e. decreasing order
		return priorities[i] > priorities[j]
	})

	ensurePrioritized := func(reapedTxs types.Txs) {
		reapedPriorities := make([]int64, len(reapedTxs))
		for i, rTx := range reapedTxs {
			reapedPriorities[i] = txMap[rTx.Key()].priority
		}

		require.Equal(t, priorities[:len(reapedPriorities)], reapedPriorities)
	}

	// reap all transactions
	reapedTxs := txmp.ReapMaxTxs(-1)
	ensurePrioritized(reapedTxs)
	require.Equal(t, len(tTxs), txmp.Size())
	require.Equal(t, int64(5800), txmp.SizeBytes())
	require.Len(t, reapedTxs, len(tTxs))

	// reap a single transaction
	reapedTxs = txmp.ReapMaxTxs(1)
	ensurePrioritized(reapedTxs)
	require.Equal(t, len(tTxs), txmp.Size())
	require.Equal(t, int64(5800), txmp.SizeBytes())
	require.Len(t, reapedTxs, 1)

	// reap half of the transactions
	reapedTxs = txmp.ReapMaxTxs(len(tTxs) / 2)
	ensurePrioritized(reapedTxs)
	require.Equal(t, len(tTxs), txmp.Size())
	require.Equal(t, int64(5800), txmp.SizeBytes())
	require.Len(t, reapedTxs, len(tTxs)/2)
}

func TestTxMempool_CheckTxExceedsMaxSize(t *testing.T) {
	txmp := setup(t, 1)

	rng := rand.New(rand.NewSource(time.Now().UnixNano()))
	tx := make([]byte, txmp.config.MaxTxBytes+1)
	_, err := rng.Read(tx)
	require.NoError(t, err)

	require.Error(t, txmp.CheckTx(tx, nil, mempool.TxInfo{SenderID: 0}))

	tx = make([]byte, txmp.config.MaxTxBytes-1)
	_, err = rng.Read(tx)
	require.NoError(t, err)

	require.NoError(t, txmp.CheckTx(tx, nil, mempool.TxInfo{SenderID: 0}))
}

func TestTxMempool_CheckTxSamePeer(t *testing.T) {
	txmp := setup(t, 100)
	peerID := uint16(1)
	rng := rand.New(rand.NewSource(time.Now().UnixNano()))

	prefix := make([]byte, 20)
	_, err := rng.Read(prefix)
	require.NoError(t, err)

	tx := []byte(fmt.Sprintf("sender-0=%X=%d", prefix, 50))

	require.NoError(t, txmp.CheckTx(tx, nil, mempool.TxInfo{SenderID: peerID}))
	require.Error(t, txmp.CheckTx(tx, nil, mempool.TxInfo{SenderID: peerID}))
}

func TestTxMempool_CheckTxSameSender(t *testing.T) {
	txmp := setup(t, 100)
	peerID := uint16(1)
	rng := rand.New(rand.NewSource(time.Now().UnixNano()))

	prefix1 := make([]byte, 20)
	_, err := rng.Read(prefix1)
	require.NoError(t, err)

	prefix2 := make([]byte, 20)
	_, err = rng.Read(prefix2)
	require.NoError(t, err)

	tx1 := []byte(fmt.Sprintf("sender-0=%X=%d", prefix1, 50))
	tx2 := []byte(fmt.Sprintf("sender-0=%X=%d", prefix2, 50))

	require.NoError(t, txmp.CheckTx(tx1, nil, mempool.TxInfo{SenderID: peerID}))
	require.Equal(t, 1, txmp.Size())
	require.NoError(t, txmp.CheckTx(tx2, nil, mempool.TxInfo{SenderID: peerID}))
	require.Equal(t, 1, txmp.Size())
}

func TestTxMempool_ConcurrentTxs(t *testing.T) {
	txmp := setup(t, 100)
	rng := rand.New(rand.NewSource(time.Now().UnixNano()))
	checkTxDone := make(chan struct{})

	var wg sync.WaitGroup

	wg.Add(1)
	go func() {
		for i := 0; i < 20; i++ {
			_ = checkTxs(t, txmp, 100, 0)
			dur := rng.Intn(1000-500) + 500
			time.Sleep(time.Duration(dur) * time.Millisecond)
		}

		wg.Done()
		close(checkTxDone)
	}()

	wg.Add(1)
	go func() {
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		defer wg.Done()

		var height int64 = 1

		for range ticker.C {
			reapedTxs := txmp.ReapMaxTxs(200)
			if len(reapedTxs) > 0 {
				responses := make([]*abci.ResponseDeliverTx, len(reapedTxs))
				for i := 0; i < len(responses); i++ {
					var code uint32

					if i%10 == 0 {
						code = 100
					} else {
						code = abci.CodeTypeOK
					}

					responses[i] = &abci.ResponseDeliverTx{Code: code}
				}

				txmp.Lock()
				require.NoError(t, txmp.Update(height, reapedTxs, responses, nil, nil))
				txmp.Unlock()

				height++
			} else {
				// only return once we know we finished the CheckTx loop
				select {
				case <-checkTxDone:
					return
				default:
				}
			}
		}
	}()

	wg.Wait()
	require.Zero(t, txmp.Size())
	require.Zero(t, txmp.SizeBytes())
}

func TestTxMempool_ExpiredTxs_Timestamp(t *testing.T) {
	t.Skip("This test is flaky and needs to be fixed")
	txmp := setup(t, 5000)
	txmp.config.TTLDuration = 5 * time.Millisecond

	added1 := checkTxs(t, txmp, 10, 0)
	require.Equal(t, len(added1), txmp.Size())

	// Wait a while, then add some more transactions that should not be expired
	// when the first batch TTLs out.
	//
	// ms: 0   1   2   3   4   5   6
	//     ^           ^       ^   ^
	//     |           |       |   +-- Update (triggers pruning)
	//     |           |       +------ first batch expires
	//     |           +-------------- second batch added
	//     +-------------------------- first batch added
	//
	// The exact intervals are not important except that the delta should be
	// large relative to the cost of CheckTx (ms vs. ns is fine here).
	time.Sleep(3 * time.Millisecond)
	added2 := checkTxs(t, txmp, 10, 1)

	// Wait a while longer, so that the first batch will expire.
	time.Sleep(3 * time.Millisecond)

	// Trigger an update so that pruning will occur.
	txmp.Lock()
	defer txmp.Unlock()
	require.NoError(t, txmp.Update(txmp.height+1, nil, nil, nil, nil))

	// All the transactions in the original set should have been purged.
	for _, tx := range added1 {
		// Check that they were added to the evicted cache.
		evicted := txmp.WasRecentlyEvicted(tx.tx.Key())
		require.True(t, evicted)

		if _, ok := txmp.txByKey[tx.tx.Key()]; ok {
			t.Errorf("Transaction %X should have been purged for TTL", tx.tx.Key())
		}
		if txmp.cache.Has(tx.tx) {
			t.Errorf("Transaction %X should have been removed from the cache", tx.tx.Key())
		}
	}

	// All the transactions added later should still be around.
	for _, tx := range added2 {
		if _, ok := txmp.txByKey[tx.tx.Key()]; !ok {
			t.Errorf("Transaction %X should still be in the mempool, but is not", tx.tx.Key())
		}
	}
}

func TestGetTxByKey_GetsTx(t *testing.T) {
	txmp := setup(t, 500)
	txs := checkTxs(t, txmp, 100, 0)

	// Should get all valid txs
	for _, tx := range txs {
		txKey := tx.tx.Key()
		txFromMempool, exists := txmp.GetTxByKey(txKey)
		require.Equal(t, tx.tx, txFromMempool)
		require.True(t, exists)
	}

	// Non-existent tx should return false
	_, exists := txmp.GetTxByKey(types.Tx("non-existent-tx").Key())
	require.False(t, exists)
}

func TestTxMempool_ExpiredTxs_NumBlocks(t *testing.T) {
	txmp := setup(t, 500)
	txmp.height = 100
	txmp.config.TTLNumBlocks = 10

	tTxs := checkTxs(t, txmp, 100, 0)
	require.Equal(t, len(tTxs), txmp.Size())

	// reap 5 txs at the next height -- no txs should expire
	reapedTxs := txmp.ReapMaxTxs(5)
	responses := make([]*abci.ResponseDeliverTx, len(reapedTxs))
	for i := 0; i < len(responses); i++ {
		responses[i] = &abci.ResponseDeliverTx{Code: abci.CodeTypeOK}
	}

	txmp.Lock()
	require.NoError(t, txmp.Update(txmp.height+1, reapedTxs, responses, nil, nil))
	txmp.Unlock()

	require.Equal(t, 95, txmp.Size())

	// check more txs at height 101
	_ = checkTxs(t, txmp, 50, 1)
	require.Equal(t, 145, txmp.Size())

	// Reap 5 txs at a height that would expire all the transactions from before
	// the previous Update (height 100).
	//
	// NOTE: When we reap txs below, we do not know if we're picking txs from the
	// initial CheckTx calls or from the second round of CheckTx calls. Thus, we
	// cannot guarantee that all 95 txs are remaining that should be expired and
	// removed. However, we do know that that at most 95 txs can be expired and
	// removed.
	reapedTxs = txmp.ReapMaxTxs(5)
	responses = make([]*abci.ResponseDeliverTx, len(reapedTxs))
	for i := 0; i < len(responses); i++ {
		responses[i] = &abci.ResponseDeliverTx{Code: abci.CodeTypeOK}
	}

	txmp.Lock()
	require.NoError(t, txmp.Update(txmp.height+10, reapedTxs, responses, nil, nil))
	txmp.Unlock()

	require.GreaterOrEqual(t, txmp.Size(), 45)
}

func TestTxMempool_CheckTxPostCheckError(t *testing.T) {
	cases := []struct {
		name string
		err  error
	}{
		{
			name: "error",
			err:  errors.New("test error"),
		},
		{
			name: "no error",
			err:  nil,
		},
	}
	for _, tc := range cases {
		testCase := tc
		t.Run(testCase.name, func(t *testing.T) {
			postCheckFn := func(_ types.Tx, _ *abci.ResponseCheckTx) error {
				return testCase.err
			}
			txmp := setup(t, 1, WithPostCheck(postCheckFn))
			rng := rand.New(rand.NewSource(time.Now().UnixNano()))
			tx := make([]byte, txmp.config.MaxTxBytes-1)
			_, err := rng.Read(tx)
			require.NoError(t, err)

			callback := func(res *abci.Response) {
				checkTxRes, ok := res.Value.(*abci.Response_CheckTx)
				require.True(t, ok)
				expectedErrString := ""
				if testCase.err != nil {
					expectedErrString = testCase.err.Error()
				}
				require.Equal(t, expectedErrString, checkTxRes.CheckTx.MempoolError)
			}
			require.NoError(t, txmp.CheckTx(tx, callback, mempool.TxInfo{SenderID: 0}))
		})
	}
}

func TestRemoveBlobTx(t *testing.T) {
	txmp := setup(t, 500)
	namespaceOne := bytes.Repeat([]byte{1}, consts.NamespaceIDSize)

	originalTx := []byte{1, 2, 3, 4}
	indexWrapper, err := types.MarshalIndexWrapper(originalTx, 100)
	require.NoError(t, err)

	// create the blobTx
	b := tmproto.Blob{
		NamespaceId:      namespaceOne,
		Data:             []byte{1, 2, 3, 4, 5, 6, 7, 8, 1, 2, 3, 4, 5, 6, 7, 8, 9},
		ShareVersion:     0,
		NamespaceVersion: 0,
	}
	bTx, err := types.MarshalBlobTx(originalTx, &b)
	require.NoError(t, err)

	err = txmp.CheckTx(bTx, nil, mempool.TxInfo{})
	require.NoError(t, err)

	err = txmp.Update(1, []types.Tx{indexWrapper}, abciResponses(1, abci.CodeTypeOK), nil, nil)
	require.NoError(t, err)
	assert.EqualValues(t, 0, txmp.Size())
	assert.EqualValues(t, 0, txmp.SizeBytes())
}

func abciResponses(n int, code uint32) []*abci.ResponseDeliverTx {
	responses := make([]*abci.ResponseDeliverTx, 0, n)
	for i := 0; i < n; i++ {
		responses = append(responses, &abci.ResponseDeliverTx{Code: code})
	}
	return responses
}
