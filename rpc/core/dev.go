package core

import (
	ctypes "github.com/KYVENetwork/celestia-core/rpc/core/types"
	rpctypes "github.com/KYVENetwork/celestia-core/rpc/jsonrpc/types"
)

// UnsafeFlushMempool removes all transactions from the mempool.
func UnsafeFlushMempool(ctx *rpctypes.Context) (*ctypes.ResultUnsafeFlushMempool, error) {
	GetEnvironment().Mempool.Flush()
	return &ctypes.ResultUnsafeFlushMempool{}, nil
}
