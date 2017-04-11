// autogenerated: do not edit!
// generated from gentemplate [gentemplate -d Package=ixge -id tx_dma_queue -d VecType=tx_dma_queue_vec -d Type=tx_dma_queue github.com/platinasystems/go/elib/vec.tmpl]

// Copyright 2016 Platina Systems, Inc. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package ixge

import (
	"github.com/platinasystems/go/elib"
)

type tx_dma_queue_vec []tx_dma_queue

func (p *tx_dma_queue_vec) Resize(n uint) {
	c := elib.Index(cap(*p))
	l := elib.Index(len(*p)) + elib.Index(n)
	if l > c {
		c = elib.NextResizeCap(l)
		q := make([]tx_dma_queue, l, c)
		copy(q, *p)
		*p = q
	}
	*p = (*p)[:l]
}

func (p *tx_dma_queue_vec) validate(new_len uint, zero tx_dma_queue) *tx_dma_queue {
	c := elib.Index(cap(*p))
	lʹ := elib.Index(len(*p))
	l := elib.Index(new_len)
	if l <= c {
		// Need to reslice to larger length?
		if l > lʹ {
			*p = (*p)[:l]
			for i := lʹ; i < l; i++ {
				(*p)[i] = zero
			}
		}
		return &(*p)[l-1]
	}
	return p.validateSlowPath(zero, c, l, lʹ)
}

func (p *tx_dma_queue_vec) validateSlowPath(zero tx_dma_queue, c, l, lʹ elib.Index) *tx_dma_queue {
	if l > c {
		cNext := elib.NextResizeCap(l)
		q := make([]tx_dma_queue, cNext, cNext)
		copy(q, *p)
		for i := c; i < cNext; i++ {
			q[i] = zero
		}
		*p = q[:l]
	}
	if l > lʹ {
		*p = (*p)[:l]
	}
	return &(*p)[l-1]
}

func (p *tx_dma_queue_vec) Validate(i uint) *tx_dma_queue {
	var zero tx_dma_queue
	return p.validate(i+1, zero)
}

func (p *tx_dma_queue_vec) ValidateInit(i uint, zero tx_dma_queue) *tx_dma_queue {
	return p.validate(i+1, zero)
}

func (p *tx_dma_queue_vec) ValidateLen(l uint) (v *tx_dma_queue) {
	if l > 0 {
		var zero tx_dma_queue
		v = p.validate(l, zero)
	}
	return
}

func (p *tx_dma_queue_vec) ValidateLenInit(l uint, zero tx_dma_queue) (v *tx_dma_queue) {
	if l > 0 {
		v = p.validate(l, zero)
	}
	return
}

func (p tx_dma_queue_vec) Len() uint { return uint(len(p)) }
