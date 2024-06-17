package core

import (
	ctypes "github.com/KYVENetwork/tendermint/rpc/core/types"
	rpctypes "github.com/KYVENetwork/tendermint/rpc/jsonrpc/types"
)

// UnsafeFlushMempool removes all transactions from the mempool.
func UnsafeFlushMempool(ctx *rpctypes.Context) (*ctypes.ResultUnsafeFlushMempool, error) {
	GetEnvironment().Mempool.Flush()
	return &ctypes.ResultUnsafeFlushMempool{}, nil
}
