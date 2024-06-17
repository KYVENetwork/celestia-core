package v1

import (
	"github.com/KYVENetwork/celestia-core/abci/example/kvstore"
	"github.com/KYVENetwork/celestia-core/config"
	"github.com/KYVENetwork/celestia-core/libs/log"
	mempl "github.com/KYVENetwork/celestia-core/mempool"
	"github.com/KYVENetwork/celestia-core/proxy"

	mempoolv1 "github.com/KYVENetwork/celestia-core/mempool/v1"
)

var mempool mempl.Mempool

func init() {
	app := kvstore.NewApplication()
	cc := proxy.NewLocalClientCreator(app)
	appConnMem, _ := cc.NewABCIClient()
	err := appConnMem.Start()
	if err != nil {
		panic(err)
	}
	cfg := config.DefaultMempoolConfig()
	cfg.Broadcast = false
	log := log.NewNopLogger()
	mempool = mempoolv1.NewTxMempool(log, cfg, appConnMem, 0)
}

func Fuzz(data []byte) int {

	err := mempool.CheckTx(data, nil, mempl.TxInfo{})
	if err != nil {
		return 0
	}

	return 1
}
