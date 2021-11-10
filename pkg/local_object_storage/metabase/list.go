package meta

import (
	"fmt"
	"strings"

	"github.com/nspcc-dev/neofs-api-go/pkg/object"
	core "github.com/nspcc-dev/neofs-node/pkg/core/object"
	"go.etcd.io/bbolt"
)

// ListPrm contains parameters for ListWithCursor operation.
type ListPrm struct {
	count  int
	cursor string
}

// WithCount sets maximum amount of addresses that ListWithCursor can return.
func (l *ListPrm) WithCount(count uint32) *ListPrm {
	l.count = int(count)
	return l
}

// WithCursor sets cursor for ListWithCursor operation. For initial request
// ignore this param or use empty string. For continues requests, use value
// from ListRes.
func (l *ListPrm) WithCursor(cursor string) *ListPrm {
	l.cursor = cursor
	return l
}

// ListRes contains values returned from ListWithCursor operation.
type ListRes struct {
	addrList []*object.Address
	cursor   string
}

// AddressList returns addresses selected by ListWithCursor operation.
func (l ListRes) AddressList() []*object.Address {
	return l.addrList
}

// Cursor returns cursor for consecutive listing requests.
func (l ListRes) Cursor() string {
	return l.cursor
}

const (
	cursorPrefixPrimary   = 'p'
	cursorPrefixTombstone = 't'
	cursorPrefixSG        = 's'
)

// ListWithCursor lists physical objects available in metabase. Includes regular,
// tombstone and storage group objects. Does not include inhumed objects. Use
// cursor value from response for consecutive requests.
func ListWithCursor(db *DB, count uint32, cursor string) ([]*object.Address, string, error) {
	r, err := db.ListWithCursor(new(ListPrm).WithCount(count).WithCursor(cursor))
	if err != nil {
		return nil, "", err
	}

	return r.AddressList(), r.Cursor(), nil
}

// ListWithCursor lists physical objects available in metabase. Includes regular,
// tombstone and storage group objects. Does not include inhumed objects. Use
// cursor value from response for consecutive requests.
func (db *DB) ListWithCursor(prm *ListPrm) (res *ListRes, err error) {
	err = db.boltDB.View(func(tx *bbolt.Tx) error {
		res = new(ListRes)
		res.addrList, res.cursor, err = db.listWithCursor(tx, prm.count, prm.cursor)
		return err
	})

	return res, err
}

func (db *DB) listWithCursor(tx *bbolt.Tx, count int, cursor string) ([]*object.Address, string, error) {
	threshold := len(cursor) == 0 // threshold is a flag to ignore cursor
	a := object.NewAddress()
	var cursorPrefix uint8

	if !threshold { // if cursor is present, then decode it and check sanity
		cursorPrefix = cursor[0]
		switch cursorPrefix {
		case cursorPrefixPrimary, cursorPrefixSG, cursorPrefixTombstone:
		default:
			return nil, "", fmt.Errorf("invalid cursor prefix %s", string(cursorPrefix))
		}

		cursor = cursor[1:]
		if err := a.Parse(cursor); err != nil {
			return nil, "", fmt.Errorf("invalid cursor address: %w", err)
		}
	}

	result := make([]*object.Address, 0, count)
	unique := make(map[string]struct{}) // do not parse the same containerID twice

	c := tx.Cursor()
	name, _ := c.First()

	if !threshold {
		name, _ = c.Seek([]byte(a.ContainerID().String()))
	}

loop:
	for ; name != nil; name, _ = c.Next() {
		containerID := parseContainerID(name, unique)
		if containerID == nil {
			continue
		}

		unique[containerID.String()] = struct{}{}
		prefix := containerID.String() + "/"

		lookupBuckets := [...]struct {
			name   []byte
			prefix uint8
		}{
			{primaryBucketName(containerID), cursorPrefixPrimary},
			{tombstoneBucketName(containerID), cursorPrefixTombstone},
			{storageGroupBucketName(containerID), cursorPrefixSG},
		}

		for _, lb := range lookupBuckets {
			if !threshold && cursorPrefix != lb.prefix {
				continue // start from the bucket, specified in the cursor prefix
			}

			cursorPrefix = lb.prefix
			result, cursor = selectNFromBucket(tx, lb.name, prefix, result, count, cursor, threshold)
			if len(result) >= count {
				break loop
			}

			// set threshold flag after first `selectNFromBucket` invocation
			// first invocation must look for cursor object
			threshold = true
		}
	}

	if len(result) == 0 {
		return nil, "", core.ErrEndOfListing
	}

	return result, string(cursorPrefix) + cursor, nil
}

// selectNFromBucket similar to selectAllFromBucket but uses cursor to find
// object to start selecting from. Ignores inhumed objects.
func selectNFromBucket(tx *bbolt.Tx,
	name []byte, // bucket name
	prefix string, // string of CID, optimization
	to []*object.Address, // listing result
	limit int, // stop listing at `limit` items in result
	cursor string, // start from cursor object
	threshold bool, // ignore cursor and start immediately
) ([]*object.Address, string) {
	bkt := tx.Bucket(name)
	if bkt == nil {
		return to, cursor
	}

	count := len(to)

	c := bkt.Cursor()
	k, _ := c.First()

	if !threshold {
		seekKey := strings.Replace(cursor, prefix, "", 1)
		c.Seek([]byte(seekKey))
		k, _ = c.Next() // we are looking for objects _after_ the cursor
	}

	for ; k != nil; k, _ = c.Next() {
		if count >= limit {
			break
		}

		key := prefix + string(k)
		cursor = key

		a := object.NewAddress()
		if err := a.Parse(key); err != nil {
			break
		}

		if inGraveyard(tx, a) > 0 {
			continue
		}

		to = append(to, a)
		count++
	}

	return to, cursor
}