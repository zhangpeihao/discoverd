package daemon

import (
	etcd_error "github.com/coreos/etcd/error"
	"github.com/coreos/go-etcd/etcd"
)

const (
	EcodeKeyNotFound = etcd_error.EcodeKeyNotFound
	EcodeTestFailed  = etcd_error.EcodeTestFailed
	EcodeNotFile     = etcd_error.EcodeNotFile

	EcodeNotDir    = etcd_error.EcodeNotDir
	EcodeNodeExist = etcd_error.EcodeNodeExist

	EcodeRootROnly   = etcd_error.EcodeRootROnly
	EcodeDirNotEmpty = etcd_error.EcodeDirNotEmpty

	EcodePrevValueRequired = etcd_error.EcodePrevValueRequired
	EcodeTTLNaN            = etcd_error.EcodeTTLNaN
	EcodeIndexNaN          = etcd_error.EcodeIndexNaN

	EcodeInvalidField = etcd_error.EcodeInvalidField
	EcodeInvalidForm  = etcd_error.EcodeInvalidForm

	EcodeRaftInternal = etcd_error.EcodeRaftInternal
	EcodeLeaderElect  = etcd_error.EcodeLeaderElect

	EcodeWatcherCleared    = etcd_error.EcodeWatcherCleared
	EcodeEventIndexCleared = etcd_error.EcodeEventIndexCleared

	ErrCodeEtcdNotReachable = etcd.ErrCodeEtcdNotReachable
)

func getEtcdErrorCode(err error) int {
	if etcdErr, ok := err.(*etcd.EtcdError); ok {
		return etcdErr.ErrorCode
	}
	return 0
}
