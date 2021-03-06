package gc

import (
	bstore "github.com/ipfs/go-ipfs/blocks/blockstore"
	bserv "github.com/ipfs/go-ipfs/blockservice"
	offline "github.com/ipfs/go-ipfs/exchange/offline"
	dag "github.com/ipfs/go-ipfs/merkledag"
	pin "github.com/ipfs/go-ipfs/pin"
	key "gx/ipfs/Qmce4Y4zg3sYr7xKM5UueS67vhNni6EeWgCRnb7MbLJMew/go-key"

	logging "gx/ipfs/QmSpJByNKFX1sCsHBEp3R73FL4NF6FnQTEGyNAXHm2GS52/go-log"
	context "gx/ipfs/QmZy2y8t9zQH2a1b8q2ZSLKp17ATuJoCNxxyMFG5qFExpt/go-net/context"
	cid "gx/ipfs/QmfSc2xehWmWLnwwYR91Y8QF4xdASypTFVknutoKQS3GHp/go-cid"
)

var log = logging.Logger("gc")

// GC performs a mark and sweep garbage collection of the blocks in the blockstore
// first, it creates a 'marked' set and adds to it the following:
// - all recursively pinned blocks, plus all of their descendants (recursively)
// - bestEffortRoots, plus all of its descendants (recursively)
// - all directly pinned blocks
// - all blocks utilized internally by the pinner
//
// The routine then iterates over every block in the blockstore and
// deletes any block that is not found in the marked set.
func GC(ctx context.Context, bs bstore.GCBlockstore, pn pin.Pinner, bestEffortRoots []*cid.Cid) (<-chan key.Key, error) {
	unlocker := bs.GCLock()

	bsrv := bserv.New(bs, offline.Exchange(bs))
	ds := dag.NewDAGService(bsrv)

	gcs, err := ColoredSet(ctx, pn, ds, bestEffortRoots)
	if err != nil {
		return nil, err
	}

	keychan, err := bs.AllKeysChan(ctx)
	if err != nil {
		return nil, err
	}

	output := make(chan key.Key)
	go func() {
		defer close(output)
		defer unlocker.Unlock()
		for {
			select {
			case k, ok := <-keychan:
				if !ok {
					return
				}
				if !gcs.Has(k) {
					err := bs.DeleteBlock(k)
					if err != nil {
						log.Debugf("Error removing key from blockstore: %s", err)
						return
					}
					select {
					case output <- k:
					case <-ctx.Done():
						return
					}
				}
			case <-ctx.Done():
				return
			}
		}
	}()

	return output, nil
}

func Descendants(ctx context.Context, ds dag.DAGService, set key.KeySet, roots []*cid.Cid, bestEffort bool) error {
	for _, c := range roots {
		set.Add(key.Key(c.Hash()))
		nd, err := ds.Get(ctx, c)
		if err != nil {
			return err
		}

		// EnumerateChildren recursively walks the dag and adds the keys to the given set
		err = dag.EnumerateChildren(ctx, ds, nd, func(c *cid.Cid) bool {
			k := key.Key(c.Hash())
			seen := set.Has(k)
			if seen {
				return false
			}
			set.Add(k)
			return true
		}, bestEffort)
		if err != nil {
			return err
		}
	}

	return nil
}

func ColoredSet(ctx context.Context, pn pin.Pinner, ds dag.DAGService, bestEffortRoots []*cid.Cid) (key.KeySet, error) {
	// KeySet currently implemented in memory, in the future, may be bloom filter or
	// disk backed to conserve memory.
	gcs := key.NewKeySet()
	err := Descendants(ctx, ds, gcs, pn.RecursiveKeys(), false)
	if err != nil {
		return nil, err
	}

	err = Descendants(ctx, ds, gcs, bestEffortRoots, true)
	if err != nil {
		return nil, err
	}

	for _, k := range pn.DirectKeys() {
		gcs.Add(key.Key(k.Hash()))
	}

	err = Descendants(ctx, ds, gcs, pn.InternalPins(), false)
	if err != nil {
		return nil, err
	}

	return gcs, nil
}
