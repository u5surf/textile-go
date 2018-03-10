package core

import (
	"errors"
	routing "gx/ipfs/QmTiWLZ6Fo5j4KcTVutZJ5KWRRJrbxzmxA4td8NfEdrPh7/go-libp2p-routing"
	peer "gx/ipfs/QmQnuSxgSFubscHgkgSeayLxKmVcmNhFUaZw4gHtV3tJ15/go-libp2p-peer"
	libp2p "gx/ipfs/Qmb9aAJwV1mDc5iPNtVuzVvsNiKA6kkDpZspMUgVfXPVc8/go-libp2p-crypto"
	"path"
	"time"

	"github.com/op/go-logging"
	"github.com/textileio/textile-go/ipfs"
	"github.com/textileio/textile-go/namesys"
	"github.com/textileio/textile-go/net"
	"github.com/textileio/textile-go/repo"
	"golang.org/x/net/context"
	ds "gx/ipfs/QmXRKBQA4wXP7xWbFiZsR1GP4HV6wMDQ1aWFxZZ4uBcPX9/go-datastore"
	"gx/ipfs/QmXporsyf5xMvffd2eiTDoq85dNpYUynGJhfabzDjwP8uR/go-ipfs/commands"
	"gx/ipfs/QmXporsyf5xMvffd2eiTDoq85dNpYUynGJhfabzDjwP8uR/go-ipfs/core"
	"sync"
)

var (
	VERSION   = "0.0.1"
	USERAGENT = "/textile-go:" + VERSION + "/"
)

const (
	CachePrefix    = "IPNSPERSISENTCACHE_"
	KeyCachePrefix = "IPNSPUBKEYCACHE_"
)

var log = logging.MustGetLogger("core")

var Node *TextileNode

var inflightPublishRequests int

type TextileNode struct {
	// Context for issuing IPFS commands
	Context commands.Context

	// IPFS node object
	IpfsNode *core.IpfsNode

	/* The roothash of the node directory inside the openbazaar repo.
	   This directory hash is published on IPNS at our peer ID making
	   the directory publicly viewable on the network. */
	RootHash string

	// The path to the openbazaar repo in the file system
	RepoPath string

	// Database for storing node specific data
	Datastore repo.Datastore

	// Websocket channel used for pushing data to the UI
	Broadcast chan interface{}

	// Used to resolve domains to OpenBazaar IDs
	NameSystem *namesys.NameSystem

	// Optional nodes to push user data to
	PushNodes []peer.ID

	// The user-agent for this node
	UserAgent string

	//// Allow other nodes to push data to this node for storage
	//AcceptStoreRequests bool

	// Last ditch API to find records that dropped out of the DHT
	IPNSBackupAPI string

	TestnetEnable        bool
	RegressionTestEnable bool
}

// Unpin the current node repo, re-add it, then publish to IPNS
var seedLock sync.Mutex
var PublishLock sync.Mutex
var InitalPublishComplete bool = false

// TestNetworkEnabled indicates whether the node is operating with test parameters
func (n *TextileNode) TestNetworkEnabled() bool { return n.TestnetEnable }

// RegressionNetworkEnabled indicates whether the node is operating with regression parameters
func (n *TextileNode) RegressionNetworkEnabled() bool { return n.RegressionTestEnable }

func (n *TextileNode) SeedNode() error {
	seedLock.Lock()
	ipfs.UnPinDir(n.Context, n.RootHash)
	var aerr error
	var rootHash string
	// There's an IPFS bug on Windows that might be related to the Windows indexer that could cause this to fail
	// If we fail the first time, let's retry a couple times before giving up.
	for i := 0; i < 3; i++ {
		rootHash, aerr = ipfs.AddDirectory(n.Context, path.Join(n.RepoPath, "root"))
		if aerr == nil {
			break
		}
		time.Sleep(time.Millisecond * 500)
	}
	if aerr != nil {
		seedLock.Unlock()
		return aerr
	}
	n.RootHash = rootHash
	seedLock.Unlock()
	InitalPublishComplete = true
	go n.publish(rootHash)
	return nil
}

func (n *TextileNode) publish(hash string) {
	// Multiple publishes may have been queued
	// We only need to publish the most recent
	PublishLock.Lock()
	defer PublishLock.Unlock()
	if hash != n.RootHash {
		return
	}

	if inflightPublishRequests == 0 {
		//n.Broadcast <- notifications.StatusNotification{"publishing"}
	}

	//id, err := cid.Decode(hash)
	//if err != nil {
	//	log.Error(err)
	//	return
	//}

	//var graph []cid.Cid
	//if len(n.PushNodes) > 0 {
	//	graph, err = ipfs.FetchGraph(n.IpfsNode.DAG, id)
	//	if err != nil {
	//		log.Error(err)
	//		return
	//	}
	//	pointers, err := n.Datastore.Pointers().GetByPurpose(ipfs.MESSAGE)
	//	if err != nil {
	//		log.Error(err)
	//		return
	//	}
	//	// Check if we're seeding any outgoing messages and add their CIDs to the graph
	//	for _, p := range pointers {
	//		if len(p.Value.Addrs) > 0 {
	//			s, err := p.Value.Addrs[0].ValueForProtocol(ma.P_IPFS)
	//			if err != nil {
	//				continue
	//			}
	//			c, err := cid.Decode(s)
	//			if err != nil {
	//				continue
	//			}
	//			graph = append(graph, *c)
	//		}
	//	}
	//}
	//for _, p := range n.PushNodes {
	//	go func(pid peer.ID) {
	//		err := n.SendStore(pid.Pretty(), graph)
	//		if err != nil {
	//			log.Errorf("Error pushing data to peer %s: %s", pid.Pretty(), err.Error())
	//		}
	//	}(p)
	//}

	inflightPublishRequests++
	_, err := ipfs.Publish(n.Context, hash)

	inflightPublishRequests--
	if inflightPublishRequests == 0 {
		if err != nil {
			log.Error(err)
			//n.Broadcast <- notifications.StatusNotification{"error publishing"}
		} else {
			//n.Broadcast <- notifications.StatusNotification{"publish complete"}
		}
	}
}

/* This is a placeholder until the libsignal is operational.
   For now we will just encrypt outgoing offline messages with the long lived identity key.
   Optionally you may provide a public key, to avoid doing an IPFS lookup */
func (n *TextileNode) EncryptMessage(peerID peer.ID, peerKey *libp2p.PubKey, message []byte) (ct []byte, rerr error) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if peerKey == nil {
		var pubKey libp2p.PubKey
		keyval, err := n.IpfsNode.Repo.Datastore().Get(ds.NewKey(KeyCachePrefix + peerID.String()))
		if err != nil {
			pubKey, err = routing.GetPublicKey(n.IpfsNode.Routing, ctx, []byte(peerID))
			if err != nil {
				log.Errorf("Failed to find public key for %s", peerID.Pretty())
				return nil, err
			}
		} else {
			pubKey, err = libp2p.UnmarshalPublicKey(keyval.([]byte))
			if err != nil {
				log.Errorf("Failed to find public key for %s", peerID.Pretty())
				return nil, err
			}
		}
		peerKey = &pubKey
	}
	if peerID.MatchesPublicKey(*peerKey) {
		ciphertext, err := net.Encrypt(*peerKey, message)
		if err != nil {
			return nil, err
		}
		return ciphertext, nil
	} else {
		log.Errorf("peer public key and id do not match for peer: %s", peerID.Pretty())
		return nil, errors.New("peer public key and id do not match")
	}
}