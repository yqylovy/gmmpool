package gmmpool

import (
	"errors"
	"io"
	"sort"
	"sync"
)

var (
	ErrBufNotEnough = errors.New("buffer not enough")
)

type Buffer struct {
	buf  []byte
	next *Buffer // next free buffer
	off  int     // read at &buf[off], write at &buf[len(buf)]
	size int     // maximum buf size

	block, idx int
}

func (b *Buffer) Bytes() []byte {
	return b.buf
}

func (b *Buffer) Reset() {
	b.buf = b.buf[:0]
	b.off = 0
}

func (b *Buffer) Read(p []byte) (n int, err error) {
	if b.off >= len(b.buf) {
		b.Reset()
		if len(p) == 0 {
			return
		}
		return 0, io.EOF
	}
	n = copy(p, b.buf[b.off:])
	return
}

func (b *Buffer) Write(p []byte) (n int, err error) {
	need_size := len(p) + len(b.buf)
	if need_size > b.size {
		return 0, ErrBufNotEnough
	}
	m := len(b.buf)
	b.buf = b.buf[:need_size]
	return copy(b.buf[m:need_size], p), nil
}

func (b *Buffer) ReadAll(r io.Reader) ([]byte, error) {
	if b.off >= b.size {
		b.Reset()
	}
	for {
		m, err := r.Read(b.buf[len(b.buf):b.size])
		b.buf = b.buf[:len(b.buf)+m]
		if err == io.EOF {
			break
		}
		if err != nil {
			return b.Bytes(), err
		}
		if m == 0 && len(b.buf) == b.size {
			return b.Bytes(), ErrBufNotEnough
		}
	}
	return b.Bytes(), nil // err is EOF, so return nil explicitly
}

type block struct {
	data   []byte
	unused []*Buffer
	free   *Buffer
	used   int
	num    int
	size   int
}

func newBlock(blockidx int, num, size int) *block {
	blk := &block{
		data: make([]byte, num*size),
		num:  num,
		size: size,
	}
	blk.init(blockidx)
	return blk
}

func (blk *block) init(blockidx int) {
	var (
		i   int
		b   *Buffer
		bs  []Buffer
		buf []byte
	)
	buf = make([]byte, blk.num*blk.size)
	bs = make([]Buffer, blk.num)
	blk.free = &bs[0]
	b = blk.free
	for i = 0; i < blk.num; i++ {
		b.buf = buf[i*blk.size : i*blk.size]
		b.size = blk.size

		b.block = blockidx
		b.idx = i
		if i != blk.num-1 {
			b.next = &bs[i+1]
			b = b.next
		}
	}
}

func (blk *block) get() *Buffer {
	blk.used++
	ret := blk.free
	blk.free = ret.next
	return ret
}
func (blk *block) release(buf *Buffer) {
	blk.used--
	buf.next = blk.free
	blk.free = buf
	buf.Reset()
}
func (blk *block) full() bool  { return blk.used == blk.num }
func (blk *block) empty() bool { return blk.used == 0 }

type Pool struct {
	sync.Mutex
	blocks []*block
	num    int
	size   int
}

func NewPool(num, size int) (p *Pool) {
	return &Pool{
		blocks: []*block{newBlock(0, num, size)},
		num:    num,
		size:   size,
	}
}

func (pool *Pool) Get() (buf *Buffer) {
	pool.Lock()
	for _, blk := range pool.blocks {
		if !blk.full() {
			buf = blk.get()
			pool.Unlock()
			return
		}
	}
	// all block is full,add new
	var num int
	l := len(pool.blocks)
	if l > 3 {
		num = pool.num * 4
	} else {
		num = pool.blocks[l-1].num * 2
	}
	blk := newBlock(l, num, pool.size)
	pool.blocks = append(pool.blocks, blk)
	buf = blk.get()
	pool.Unlock()
	return
}
func (pool *Pool) Put(buf *Buffer) {
	pool.Lock()
	blk := pool.blocks[buf.block]
	blk.release(buf)
	if buf.block == len(pool.blocks) && blk.empty() {
		pool.blocks = pool.blocks[:buf.block-1]
	}
	pool.Unlock()
}

type MultiLevelPool struct {
	pools []*Pool
}

type PoolOpt struct {
	Num  int
	Size int
}

type PoolOptList []PoolOpt

func (a PoolOptList) Len() int           { return len(a) }
func (a PoolOptList) Swap(i, j int)      { a[i], a[j] = a[j], a[i] }
func (a PoolOptList) Less(i, j int) bool { return a[i].Size < a[j].Size }

func NewMultiLevelPool(opts []PoolOpt) *MultiLevelPool {
	optList := PoolOptList(opts)
	sort.Sort(optList)

	pools := make([]*Pool, optList.Len())
	for i, opt := range optList {
		pools[i] = NewPool(opt.Num, opt.Size)
	}
	return &MultiLevelPool{
		pools: pools,
	}
}

func (mlp *MultiLevelPool) Get(size int) (b *Buffer) {
	for _, p := range mlp.pools {
		if p.size >= size {
			return p.Get()
		}
	}

	return nil
}

func (mlp *MultiLevelPool) Put(b *Buffer) {
	for _, p := range mlp.pools {
		if p.size == b.size {
			p.Put(b)
		}
	}
}
