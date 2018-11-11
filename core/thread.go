package core

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/golang/protobuf/proto"
	"github.com/golang/protobuf/ptypes"
	"github.com/textileio/textile-go/crypto"
	"github.com/textileio/textile-go/ipfs"
	"github.com/textileio/textile-go/pb"
	"github.com/textileio/textile-go/repo"
	"github.com/textileio/textile-go/repo/config"
	"github.com/textileio/textile-go/schema"
	mh "gx/ipfs/QmPnFwZ2JXKnXgMw8CdBPxn7FWh6LLdjUjxV1fKHuJnkr8/go-multihash"
	"gx/ipfs/QmdVrMn1LhB4ybb8hMVaMLXnA8XRSewMnK6YqXKXoTcRvN/go-libp2p-peer"
	libp2pc "gx/ipfs/Qme1knMqwt1hKZbc1BmQFmnm9f36nyQGwXxPGVpVJ9rMK5/go-libp2p-crypto"
	"gx/ipfs/QmebqVUQQqQFhg74FtQFszUJo22Vpr3e8qBAkvvV4ho9HH/go-ipfs/core"
	"strings"
	"sync"
	"time"
)

// errReloadFailed indicates an error occurred during thread reload
var errThreadReload = errors.New("could not re-load thread")

// ErrInvitesNotAllowed indicates an invite was attempted on a private thread
var ErrInvitesNotAllowed = errors.New("invites not allowed to private thread")

// ErrDAGSchemaRequired indicates files where added without a thread schema
var ErrDAGSchemaRequired = errors.New("DAG schema required to add files")

// ThreadUpdate is used to notify listeners about updates in a thread
type ThreadUpdate struct {
	Block      repo.Block `json:"block"`
	ThreadId   string     `json:"thread_id"`
	ThreadName string     `json:"thread_name"`
}

// ThreadInfo reports info about a thread
type ThreadInfo struct {
	Id         string       `json:"id"`
	Key        string       `json:"key"`
	Name       string       `json:"name"`
	SchemaId   string       `json:"schema_id,omitempty"`
	Schema     *schema.Node `json:"schema,omitempty"`
	Type       string       `json:"type"`
	State      string       `json:"state"`
	Head       *BlockInfo   `json:"head,omitempty"`
	PeerCount  int          `json:"peer_cnt"`
	BlockCount int          `json:"block_cnt"`
	FileCount  int          `json:"file_cnt"`
}

// BlockInfo is a more readable version of repo.Block
type BlockInfo struct {
	Id       string    `json:"id"`
	ThreadId string    `json:"thread_id"`
	AuthorId string    `json:"author_id"`
	Type     string    `json:"type"`
	Date     time.Time `json:"date"`
	Parents  []string  `json:"parents"`
	Target   string    `json:"target,omitempty"`
	Body     string    `json:"body,omitempty"`
}

// ThreadConfig is used to construct a Thread
type ThreadConfig struct {
	RepoPath      string
	Config        *config.Config
	Node          func() *core.IpfsNode
	Datastore     repo.Datastore
	Service       func() *ThreadsService
	ThreadsOutbox *ThreadsOutbox
	CafeOutbox    *CafeOutbox
	SendUpdate    func(update ThreadUpdate)
}

// Thread is the primary mechanism representing a collecion of data / files / photos
type Thread struct {
	Id            string
	Key           string // app key, usually UUID
	Name          string
	Type          repo.ThreadType
	schema        *schema.Node
	schemaId      string
	privKey       libp2pc.PrivKey
	repoPath      string
	config        *config.Config
	node          func() *core.IpfsNode
	datastore     repo.Datastore
	service       func() *ThreadsService
	threadsOutbox *ThreadsOutbox
	cafeOutbox    *CafeOutbox
	sendUpdate    func(update ThreadUpdate)
	mux           sync.Mutex
}

// NewThread create a new Thread from a repo model and config
func NewThread(model *repo.Thread, conf *ThreadConfig) (*Thread, error) {
	sk, err := libp2pc.UnmarshalPrivateKey(model.PrivKey)
	if err != nil {
		return nil, err
	}

	var sch *schema.Node
	if model.Schema != "" {
		sch, err = loadSchema(conf.Node(), model.Schema)
		if err != nil {
			return nil, err
		}
	}

	return &Thread{
		Id:            model.Id,
		Key:           model.Key,
		Name:          model.Name,
		Type:          model.Type,
		schema:        sch,
		schemaId:      model.Schema,
		privKey:       sk,
		repoPath:      conf.RepoPath,
		config:        conf.Config,
		node:          conf.Node,
		datastore:     conf.Datastore,
		service:       conf.Service,
		threadsOutbox: conf.ThreadsOutbox,
		cafeOutbox:    conf.CafeOutbox,
		sendUpdate:    conf.SendUpdate,
	}, nil
}

// Info returns thread info
func (t *Thread) Info() (*ThreadInfo, error) {
	mod := t.datastore.Threads().Get(t.Id)
	if mod == nil {
		return nil, errThreadReload
	}

	if t.schema != nil {

	}

	var head *BlockInfo
	if mod.Head != "" {
		h := t.datastore.Blocks().Get(mod.Head)
		if h != nil {
			head = &BlockInfo{
				Id:       h.Id,
				ThreadId: h.ThreadId,
				AuthorId: h.AuthorId,
				Type:     h.Type.Description(),
				Date:     h.Date,
				Parents:  h.Parents,
				Target:   h.Target,
				Body:     h.Body,
			}
		}
	}

	state, err := t.State()
	if err != nil {
		return nil, err
	}

	blocks := t.datastore.Blocks().Count(fmt.Sprintf("threadId='%s'", t.Id))
	files := t.datastore.Blocks().Count(fmt.Sprintf("threadId='%s' and type=%d", t.Id, repo.FilesBlock))

	return &ThreadInfo{
		Id:         t.Id,
		Key:        t.Key,
		Name:       t.Name,
		SchemaId:   t.schemaId,
		Schema:     t.schema,
		Type:       mod.Type.Description(),
		State:      state.Description(),
		Head:       head,
		PeerCount:  len(t.Peers()) + 1,
		BlockCount: blocks,
		FileCount:  files,
	}, nil
}

// State returns the current thread state
func (t *Thread) State() (repo.ThreadState, error) {
	mod := t.datastore.Threads().Get(t.Id)
	if mod == nil {
		return -1, errThreadReload
	}
	return mod.State, nil
}

// Head returns content id of the latest update
func (t *Thread) Head() (string, error) {
	mod := t.datastore.Threads().Get(t.Id)
	if mod == nil {
		return "", errThreadReload
	}
	return mod.Head, nil
}

// Peers returns locally known peers in this thread
func (t *Thread) Peers() []repo.ThreadPeer {
	return t.datastore.ThreadPeers().ListByThread(t.Id)
}

// Encrypt data with thread public key
func (t *Thread) Encrypt(data []byte) ([]byte, error) {
	return crypto.Encrypt(t.privKey.GetPublic(), data)
}

// Decrypt data with thread secret key
func (t *Thread) Decrypt(data []byte) ([]byte, error) {
	return crypto.Decrypt(t.privKey, data)
}

// followParents tries to follow a list of chains of block ids, processing along the way
func (t *Thread) followParents(parents []string) error {
	for _, parent := range parents {
		if parent == "" {
			log.Debugf("found genesis block, aborting")
			continue
		}

		hash, err := mh.FromB58String(parent)
		if err != nil {
			return err
		}

		if err := t.followParent(hash); err != nil {
			log.Errorf("failed to follow parent %s: %s", parent, err)
			continue
		}
	}

	return nil
}

// followParent tries to follow a chain of block ids, processing along the way
func (t *Thread) followParent(parent mh.Multihash) error {
	ciphertext, err := ipfs.DataAtPath(t.node(), parent.B58String())
	if err != nil {
		return err
	}

	block, err := t.handleBlock(parent, ciphertext)
	if err != nil {
		return err
	}
	if block == nil {
		// exists, abort
		return nil
	}

	switch block.Type {
	case pb.ThreadBlock_MERGE:
		err = t.handleMergeBlock(parent, block)
	case pb.ThreadBlock_IGNORE:
		_, err = t.handleIgnoreBlock(parent, block)
	case pb.ThreadBlock_FLAG:
		_, err = t.handleFlagBlock(parent, block)
	case pb.ThreadBlock_JOIN:
		_, err = t.handleJoinBlock(parent, block)
	case pb.ThreadBlock_ANNOUNCE:
		_, err = t.handleAnnounceBlock(parent, block)
	case pb.ThreadBlock_LEAVE:
		err = t.handleLeaveBlock(parent, block)
	case pb.ThreadBlock_MESSAGE:
		_, err = t.handleMessageBlock(parent, block)
	case pb.ThreadBlock_FILES:
		_, err = t.handleFilesBlock(parent, block)
	case pb.ThreadBlock_COMMENT:
		_, err = t.handleCommentBlock(parent, block)
	case pb.ThreadBlock_LIKE:
		_, err = t.handleLikeBlock(parent, block)
	default:
		return errors.New(fmt.Sprintf("invalid message type: %s", block.Type))
	}
	if err != nil {
		return err
	}

	return t.followParents(block.Header.Parents)
}

// addOrUpdatePeer collects thread peers, saving them as contacts and
// saving their cafe inboxes for offline message delivery
func (t *Thread) addOrUpdatePeer(pid peer.ID, username string, inboxes []string) {
	t.datastore.ThreadPeers().Add(&repo.ThreadPeer{
		Id:       pid.Pretty(),
		ThreadId: t.Id,
		Welcomed: false,
	})

	t.datastore.Contacts().AddOrUpdate(&repo.Contact{
		Id:       pid.Pretty(),
		Username: username,
		Inboxes:  inboxes,
		Added:    time.Now(),
	})
}

// newBlockHeader creates a new header
func (t *Thread) newBlockHeader() (*pb.ThreadBlockHeader, error) {
	head, err := t.Head()
	if err != nil {
		return nil, err
	}

	pdate, err := ptypes.TimestampProto(time.Now())
	if err != nil {
		return nil, err
	}

	var parents []string
	if head != "" {
		parents = strings.Split(head, ",")
	}

	return &pb.ThreadBlockHeader{
		Date:    pdate,
		Parents: parents,
		Author:  t.node().Identity.Pretty(),
	}, nil
}

// commitResult wraps the results of a block commit
type commitResult struct {
	hash       mh.Multihash
	ciphertext []byte
	header     *pb.ThreadBlockHeader
}

// commitBlock encrypts a block with thread key (or custom method if provided) and adds it to ipfs
func (t *Thread) commitBlock(msg proto.Message, mtype pb.ThreadBlock_Type, encrypt func(plaintext []byte) ([]byte, error)) (*commitResult, error) {
	header, err := t.newBlockHeader()
	if err != nil {
		return nil, err
	}
	block := &pb.ThreadBlock{
		Header: header,
		Type:   mtype,
	}
	if msg != nil {
		payload, err := ptypes.MarshalAny(msg)
		if err != nil {
			return nil, err
		}
		block.Payload = payload
	}
	plaintext, err := proto.Marshal(block)
	if err != nil {
		return nil, err
	}

	// encrypt, falling back to thread key
	if encrypt == nil {
		encrypt = t.Encrypt
	}
	ciphertext, err := encrypt(plaintext)
	if err != nil {
		return nil, err
	}

	hash, err := t.addBlock(ciphertext)
	if err != nil {
		return nil, err
	}

	return &commitResult{hash, ciphertext, header}, nil
}

// addBlock adds to ipfs
func (t *Thread) addBlock(ciphertext []byte) (mh.Multihash, error) {
	id, err := ipfs.AddData(t.node(), bytes.NewReader(ciphertext), true)
	if err != nil {
		return nil, err
	}

	t.cafeOutbox.Add(id.Hash().B58String(), repo.CafeStoreRequest)

	return id.Hash(), nil
}

// handleBlock receives an incoming encrypted block
func (t *Thread) handleBlock(hash mh.Multihash, ciphertext []byte) (*pb.ThreadBlock, error) {
	index := t.datastore.Blocks().Get(hash.B58String())
	if index != nil {
		return nil, nil
	}

	block := new(pb.ThreadBlock)
	plaintext, err := t.Decrypt(ciphertext)
	if err != nil {
		// might be a merge block
		err2 := proto.Unmarshal(ciphertext, block)
		if err2 != nil || block.Type != pb.ThreadBlock_MERGE {
			return nil, err
		}
	} else {
		if err := proto.Unmarshal(plaintext, block); err != nil {
			return nil, err
		}
	}

	// nil payload only allowed for some types
	if block.Payload == nil && block.Type != pb.ThreadBlock_MERGE && block.Type != pb.ThreadBlock_LEAVE {
		return nil, errors.New("nil message payload")
	}

	if _, err := t.addBlock(ciphertext); err != nil {
		return nil, err
	}
	return block, nil
}

// indexBlock stores off index info for this block type
func (t *Thread) indexBlock(commit *commitResult, blockType repo.BlockType, target string, body string) error {
	date, err := ptypes.Timestamp(commit.header.Date)
	if err != nil {
		return err
	}
	index := &repo.Block{
		Id:       commit.hash.B58String(),
		Type:     blockType,
		Date:     date,
		Parents:  commit.header.Parents,
		ThreadId: t.Id,
		AuthorId: commit.header.Author,
		Target:   target,
		Body:     body,
	}
	if err := t.datastore.Blocks().Add(index); err != nil {
		return err
	}

	t.pushUpdate(*index)

	return nil
}

// handleHead determines whether or not a thread can be fast-forwarded or if a merge block is needed
// - parents are the parents of the incoming chain
func (t *Thread) handleHead(inbound mh.Multihash, parents []string) (mh.Multihash, error) {
	head, err := t.Head()
	if err != nil {
		return nil, err
	}

	// fast-forward is possible if current HEAD is equal to one of the incoming parents
	var fastForwardable bool
	if head == "" {
		fastForwardable = true
	} else {
		for _, parent := range parents {
			if head == parent {
				fastForwardable = true
			}
		}
	}
	if fastForwardable {
		// no need for a merge
		log.Debugf("fast-forwarded to %s", inbound.B58String())
		if err := t.updateHead(inbound); err != nil {
			return nil, err
		}
		return nil, nil
	}

	// needs merge
	return t.merge(inbound)
}

// updateHead updates the ref to the content id of the latest update
func (t *Thread) updateHead(head mh.Multihash) error {
	if err := t.datastore.Threads().UpdateHead(t.Id, head.B58String()); err != nil {
		return err
	}

	t.cafeOutbox.Add(t.Id, repo.CafeStoreThreadRequest)

	return nil
}

// sendWelcome sends the latest HEAD block to a set of peers
func (t *Thread) sendWelcome() error {
	peers := t.datastore.ThreadPeers().ListUnwelcomedByThread(t.Id)
	if len(peers) == 0 {
		return nil
	}

	head, err := t.Head()
	if err != nil {
		return err
	}
	if head == "" {
		return nil
	}

	ciphertext, err := ipfs.DataAtPath(t.node(), head)
	if err != nil {
		return err
	}

	hash, err := mh.FromB58String(head)
	if err != nil {
		return err
	}
	res := &commitResult{hash: hash, ciphertext: ciphertext}
	if err := t.post(res, peers); err != nil {
		return err
	}

	if err := t.datastore.ThreadPeers().WelcomeByThread(t.Id); err != nil {
		return err
	}
	for _, p := range peers {
		log.Debugf("WELCOME sent to %s at %s", p.Id, head)
	}
	return nil
}

// post publishes an encrypted message to thread peers
func (t *Thread) post(commit *commitResult, peers []repo.ThreadPeer) error {
	if len(peers) == 0 {
		// flush the storage queue—this is normally done in a thread
		// via thread message queue handling, but that won't run if there's
		// no peers to send the message to.
		t.cafeOutbox.Flush()
		return nil
	}
	env, err := t.service().NewEnvelope(t.Id, commit.hash, commit.ciphertext)
	if err != nil {
		return err
	}
	for _, tp := range peers {
		pid, err := peer.IDB58Decode(tp.Id)
		if err != nil {
			return err
		}
		if err := t.threadsOutbox.Add(pid, env); err != nil {
			return err
		}
	}

	go t.threadsOutbox.Flush()

	return nil
}

// pushUpdate pushes thread updates to UI listeners
func (t *Thread) pushUpdate(index repo.Block) {
	t.sendUpdate(ThreadUpdate{
		Block:      index,
		ThreadId:   t.Id,
		ThreadName: t.Name,
	})
}

// loadSchema loads a schema from a local file
func loadSchema(node *core.IpfsNode, id string) (*schema.Node, error) {
	data, err := ipfs.DataAtPath(node, id)
	if err != nil {
		return nil, err
	}

	var sch schema.Node
	if err := json.Unmarshal(data, &sch); err != nil {
		log.Errorf("failed to unmarshal thread schema %s: %s", id, err)
		return nil, err
	}
	return &sch, nil
}
