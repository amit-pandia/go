// Copyright 2016 Platina Systems, Inc. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package ixge

import (
	"github.com/platinasystems/go/elib"
	"github.com/platinasystems/go/elib/elog"
	"github.com/platinasystems/go/elib/hw"
	"github.com/platinasystems/go/vnet"
	"github.com/platinasystems/go/vnet/ethernet"

	"fmt"
	"unsafe"
)

type rx_dma_queue struct {
	vnet.RxDmaRing

	punt_mode bool
	punt_next rx_next

	dma_queue

	rx_desc rx_from_hw_descriptor_vec
	desc_id elib.Index
}

func (d *dev) init_rx_pool() {
	p := &d.rx_pool
	t := &p.BufferTemplate

	p.Name = fmt.Sprintf("%s rx", d.Name())

	*t = vnet.DefaultBufferPool.BufferTemplate
	t.Size = d.rx_buffer_bytes

	// Set interface for rx buffers.
	ref := (*vnet.Ref)(unsafe.Pointer(&t.Ref))
	ref.Si = d.HwIf.Si()

	d.m.Vnet.AddBufferPool(p)
}

func (d *dev) rx_dma_init(queue uint) {
	q := d.rx_queues.Validate(queue)
	q.d = d
	q.index = queue
	if q.punt_mode = d.m.Config.PuntNode != ""; q.punt_mode {
		q.punt_next = rx_next(d.m.Vnet.AddNamedNext(d, d.m.Config.PuntNode))
	}

	// DMA buffer pool init.
	if len(d.rx_pool.Name) == 0 {
		if d.rx_buffer_bytes == 0 {
			d.rx_buffer_bytes = 1024
		}
		d.rx_buffer_bytes = uint(elib.Word(d.rx_buffer_bytes).RoundPow2(1024))
		d.init_rx_pool()
	}

	if d.rx_ring_len == 0 {
		d.rx_ring_len = 12 * vnet.MaxVectorLen
	}
	q.rx_desc, q.desc_id = rx_from_hw_descriptorAllocAligned(d.rx_ring_len, log2DescriptorAlignmentBytes)
	q.len = reg(d.rx_ring_len)

	flags := vnet.RxDmaDescriptorFlags(rx_desc_is_ip4 | rx_desc_is_ip4_checksummed)
	q.RxDmaRingInit(d.m.Vnet, q, flags, log2_rx_desc_is_end_of_packet, &d.rx_pool, d.rx_ring_len)

	// Put even buffers on ring; odd buffers will be used for refill.
	{
		i := uint(0)
		ri := q.RingIndex(i)
		for i < d.rx_ring_len {
			r := q.RxDmaRing.RxRef(ri)
			q.rx_desc[i].refill(r)
			i++
			ri = ri.NextRingIndex(1)
		}
	}

	dr := q.get_regs()
	dr.descriptor_address.set(d, uint64(q.rx_desc[0].PhysAddress()))
	n_desc := reg(len(q.rx_desc))
	dr.n_descriptor_bytes.set(d, n_desc*reg(unsafe.Sizeof(q.rx_desc[0])))

	{
		v := reg(d.rx_buffer_bytes/1024) << 0
		// Set lo free descriptor interrupt threshold to 1 * 64 descriptors.
		v |= 1 << 22
		// Descriptor type: advanced one buffer descriptors.
		v |= 1 << 25
		// Drop if out of descriptors.
		v |= 1 << 28
		dr.rx_split_control.set(d, v)
	}

	// Give hardware all but last cache line of descriptors.
	q.tail_index = n_desc - n_desc_per_cache_line

	hw.MemoryBarrier()

	{
		v := dr.control.get(d)
		// prefetch threshold
		v = (v &^ (0xff << 0)) | ((64 - 4) << 0)
		// host threshold
		v = (v &^ (0xff << 8)) | (4 << 8)
		// writeback theshold
		v = (v &^ (0xff << 16)) | (4 << 16)
		dr.control.set(d, v)
	}

	{
		v := dr.cache_control.get(d)
		v |= dma_cache_control_relaxed_ordering_rx_tx_desc_fetch |
			dma_cache_control_relaxed_ordering_rx_tx_desc_writeback |
			dma_cache_control_relaxed_ordering_rx_tx_data |
			dma_cache_control_relaxed_ordering_rx_head_data
		if d.have_tph {
			v |= dma_cache_control_tph_rx_tx_desc_fetch |
				dma_cache_control_tph_rx_tx_desc_writeback |
				dma_cache_control_tph_rx_head_data
		}
		dr.cache_control.set(d, v)
	}
}

func (d *dev) rx_dma_enable(queue uint, enable bool) {
	q := &d.rx_queues[queue]
	dr := q.get_regs()
	if enable {
		q.start(d, &dr.dma_regs)
		d.regs.rx_enable.set(d, 1<<0)
	} else {
		panic("not yet")
	}
}

//go:generate gentemplate -d Package=ixge -id rx_dma_queue -d VecType=rx_dma_queue_vec -d Type=rx_dma_queue github.com/platinasystems/go/elib/vec.tmpl

type rx_dev struct {
	out                    *vnet.RefOut
	rx_queues              rx_dma_queue_vec
	rx_pool                vnet.BufferPool
	rx_next_by_layer2_type [n_ethernet_type_filter]rx_next
}

// Only advanced descriptors are supported.
type rx_to_hw_descriptor struct {
	tail_buffer_address uint64
	head_buffer_address uint64
}

func (d *rx_from_hw_descriptor) to_hw() *rx_to_hw_descriptor {
	return (*rx_to_hw_descriptor)(unsafe.Pointer(d))
}

func (d *rx_from_hw_descriptor) refill(r *vnet.Ref) {
	t := d.to_hw()
	t.tail_buffer_address = uint64(r.DataPhys())
	t.head_buffer_address = 0 // needed to clear software owned bit
}

func rx_dma_flags_x1(d0 rx_from_hw_descriptor) (f0 vnet.RxDmaDescriptorFlags, sw_owned bool) {
	f0 = vnet.RxDmaDescriptorFlags(d0.status[0]) | vnet.RxDmaDescriptorFlags(d0.status[2])<<32
	sw_owned = f0&rx_desc_is_owned_by_software != 0
	return
}

func rx_dma_flags_x4(d0, d1, d2, d3 rx_from_hw_descriptor) (f0, f1, f2, f3 vnet.RxDmaDescriptorFlags, sw_owned bool) {
	f0 = vnet.RxDmaDescriptorFlags(d0.status[0]) | vnet.RxDmaDescriptorFlags(d0.status[2])<<32
	f1 = vnet.RxDmaDescriptorFlags(d1.status[0]) | vnet.RxDmaDescriptorFlags(d1.status[2])<<32
	f2 = vnet.RxDmaDescriptorFlags(d2.status[0]) | vnet.RxDmaDescriptorFlags(d2.status[2])<<32
	f3 = vnet.RxDmaDescriptorFlags(d3.status[0]) | vnet.RxDmaDescriptorFlags(d3.status[2])<<32
	sw_owned = (f0 & f1 & f2 & f3 & rx_desc_is_owned_by_software) != 0
	return
}

const (
	// If packet is layer2 bits [2:0] contain ethernet_type_queue_filter match.
	rx_desc_is_layer2   = 1 << (4 + 11)
	rx_desc_layer2_type = 0x7 << 0

	// Valid if not layer2
	rx_desc_is_ip4     = 1 << (4 + 0)
	rx_desc_is_ip4_ext = 1 << (4 + 1)
	rx_desc_is_ip6     = 1 << (4 + 2)
	rx_desc_is_ip6_ext = 1 << (4 + 3)
	rx_desc_is_tcp     = 1 << (4 + 4)
	rx_desc_is_udp     = 1 << (4 + 5)
)

const (
	rx_desc_is_owned_by_software = 1 << (32 + iota)
	rx_desc_is_end_of_packet
	rx_desc_is_flow_director_filter_match
	rx_desc_is_vlan
	rx_desc_is_udp_checksummed
	rx_desc_is_tcp_checksummed
	rx_desc_is_ip4_checksummed
	rx_desc_not_unicast
	_
	rx_desc_is_double_vlan
)

const log2_rx_desc_is_end_of_packet = 32 + 1

// Extended errors
const (
	rx_desc_is_ethernet_error           = 1 << (32 + 20 + 9)
	rx_desc_is_udp_tcp_invalid_checksum = 1 << (32 + 20 + 10)
	rx_desc_is_ip4_invalid_checksum     = 1 << (32 + 20 + 11)
)

// Rx writeback descriptor format.
type rx_from_hw_descriptor struct {
	status [3]uint32

	n_bytes_this_descriptor uint16
	vlan_tag                uint16
}

const n_desc_per_cache_line = 4

//go:generate gentemplate -d Package=ixge -id rx_from_hw_descriptor -d Type=rx_from_hw_descriptor -d VecType=rx_from_hw_descriptor_vec github.com/platinasystems/go/elib/hw/dma_mem.tmpl

func (e *rx_from_hw_descriptor) String() (s string) {
	var (
		f        vnet.RxDmaDescriptorFlags
		sw_owned bool
	)
	// copy to avoid problems with potentially dma memory.
	d := *e
	if f, sw_owned = rx_dma_flags_x1(d); !sw_owned {
		t := d.to_hw()
		s = fmt.Sprintf("hw: head %x tail %x", t.head_buffer_address, t.tail_buffer_address)
		return
	}

	s += fmt.Sprintf("sw: %d bytes", d.n_bytes_this_descriptor)

	if f&rx_desc_is_vlan != 0 {
		s += fmt.Sprintf(", vlan %d", d.vlan_tag)
	}
	if f&rx_desc_is_double_vlan != 0 {
		s += ", double-vlan"
	}

	if f&rx_desc_is_end_of_packet != 0 {
		s += ", eop"
	}

	if f&rx_desc_is_flow_director_filter_match != 0 {
		s += ", flow-director match"
	}
	if f&rx_desc_not_unicast != 0 {
		s += ", not unicast"
	}

	if f&rx_desc_is_layer2 != 0 {
		s += fmt.Sprintf(", layer-2 %d", f&rx_desc_layer2_type)
	} else {
		if f&rx_desc_is_ip4 != 0 {
			s += fmt.Sprintf(", ip4")
			if f&rx_desc_is_ip4_checksummed != 0 {
				s += " (checksummed)"
			}
			if f&rx_desc_is_ip4_invalid_checksum != 0 {
				s += " (invalid-checksum)"
			}
		}
		if f&rx_desc_is_ip4_ext != 0 {
			s += fmt.Sprintf(", ip4-ext")
		}
		if f&rx_desc_is_ip6 != 0 {
			s += fmt.Sprintf(", ip6")
		}
		if f&rx_desc_is_ip6_ext != 0 {
			s += fmt.Sprintf(", ip6-ext")
		}
		if f&rx_desc_is_tcp != 0 {
			s += fmt.Sprintf(", tcp")
			if f&rx_desc_is_tcp_checksummed != 0 {
				s += " checksummed"
			}
			if f&rx_desc_is_udp_tcp_invalid_checksum != 0 {
				s += " invalid-checksum"
			}
		}
		if f&rx_desc_is_udp != 0 {
			s += fmt.Sprintf(", udp")
			if f&rx_desc_is_udp_checksummed != 0 {
				s += " checksummed"
			}
			if f&rx_desc_is_udp_tcp_invalid_checksum != 0 {
				s += " invalid-checksum"
			}
		}
	}
	return
}

type rx_next uint8

const (
	rx_next_error rx_next = iota
	rx_next_punt
	rx_next_punt_node
	rx_next_ethernet_input
	rx_next_ip4_input_valid_checksum
	rx_next_ip6_input
)

type rx_error uint32

const (
	rx_error_none rx_error = iota
	rx_error_ip4_invalid_checksum
)

func (q *rx_dma_queue) GetRefState(f vnet.RxDmaDescriptorFlags) (s vnet.RxDmaRefState) {
	error, next, advance := rx_error_none, rx_next_ethernet_input, 0

	var is_ip4 bool

	if q.punt_mode {
		error, next, advance = rx_error_none, q.punt_next, 0
		goto done
	}

	is_ip4 = f&rx_desc_is_ip4_checksummed != 0
	if is_ip4 {
		next = rx_next_ip4_input_valid_checksum
	}
	if f&rx_desc_is_ip4_invalid_checksum != 0 {
		error = rx_error_ip4_invalid_checksum
	}

	if f&rx_desc_is_ip6 != 0 {
		next = rx_next_ip6_input
	}
	if f&rx_desc_is_layer2 != 0 {
		next = q.d.rx_next_by_layer2_type[f&rx_desc_layer2_type]
	}

	if error != rx_error_none {
		next = rx_next_error
	}

	if next != rx_next_ethernet_input {
		advance = ethernet.SizeofHeader
		if f&rx_desc_is_vlan != 0 {
			advance += ethernet.SizeofVlanHeader
		}
		if f&rx_desc_is_double_vlan != 0 {
			advance += ethernet.SizeofVlanHeader
		}
	}

done:
	s.Next = uint(next)
	s.Si = q.d.Si()
	s.SetError(&q.d.Node, uint(error))
	s.Advance = advance
	return
}

type rx_done_code int

const (
	rx_done_not_done = iota
	rx_done_vec_len
	rx_done_found_hw_owned_descriptor
)

var rx_done_code_strings = [...]string{
	rx_done_not_done:                  "not-done",
	rx_done_vec_len:                   "vec-len",
	rx_done_found_hw_owned_descriptor: "hw-owned",
}

func (c rx_done_code) String() string { return elib.Stringer(rx_done_code_strings[:], int(c)) }

const elog_desc = false // true to add elogs for rx descriptors + packet data.

func (q *rx_dma_queue) rx_no_wrap(n_doneʹ reg, n_descriptors reg) (done rx_done_code, n_done reg) {
	d := q.d
	n_left := n_descriptors
	i := q.head_index
	n_done = n_doneʹ
	done = rx_done_not_done

	ri := q.RingIndex(uint(i))
	for n_left >= 4 && q.MaxNext()+4 <= vnet.MaxVectorLen {
		x0, x1, x2, x3 := q.rx_desc[i+0], q.rx_desc[i+1], q.rx_desc[i+2], q.rx_desc[i+3]

		// Skip to single loop for any hardware owned descriptors found.
		var (
			f0, f1, f2, f3 vnet.RxDmaDescriptorFlags
			sw_owned       bool
		)

		if elog.Enabled() && elog_desc {
			var e rx_desc_elog
			e.kind = rx_desc_elog_x4
			e.name = d.elog_name
			e.index = uint16(ri.Index())
			e.desc = x0
			r0 := ri.NextRxRef(&q.RxDmaRing, 0)
			copy(e.data[:], r0.DataSliceOffsetLen(0, 32))
			elog.Add(&e)
			e.desc = x1
			r1 := ri.NextRxRef(&q.RxDmaRing, 1)
			copy(e.data[:], r1.DataSliceOffsetLen(0, 32))
			elog.Add(&e)
			e.desc = x2
			r2 := ri.NextRxRef(&q.RxDmaRing, 2)
			copy(e.data[:], r2.DataSliceOffsetLen(0, 32))
			elog.Add(&e)
			e.desc = x3
			r3 := ri.NextRxRef(&q.RxDmaRing, 3)
			copy(e.data[:], r3.DataSliceOffsetLen(0, 32))
			elog.Add(&e)
		}

		if f0, f1, f2, f3, sw_owned = rx_dma_flags_x4(x0, x1, x2, x3); !sw_owned {
			break
		}

		b0, b1 := uint(x0.n_bytes_this_descriptor), uint(x1.n_bytes_this_descriptor)
		b2, b3 := uint(x2.n_bytes_this_descriptor), uint(x3.n_bytes_this_descriptor)

		x0.refill(q.RefillRef(ri.NextRingIndex(0)))
		x1.refill(q.RefillRef(ri.NextRingIndex(1)))
		x2.refill(q.RefillRef(ri.NextRingIndex(2)))
		x3.refill(q.RefillRef(ri.NextRingIndex(3)))

		q.rx_desc[i+0], q.rx_desc[i+1], q.rx_desc[i+2], q.rx_desc[i+3] = x0, x1, x2, x3

		q.Rx4Descriptors(ri, b0, b1, b2, b3, f0, f1, f2, f3)

		n_left -= 4
		i += 4
		ri = ri.NextRingIndex(4)
	}

	for n_left > 0 && q.MaxNext()+1 <= vnet.MaxVectorLen {
		x0 := q.rx_desc[i+0]

		var (
			f0       vnet.RxDmaDescriptorFlags
			sw_owned bool
		)

		if elog.Enabled() && elog_desc {
			var e rx_desc_elog
			e.kind = rx_desc_elog_x1
			e.name = d.elog_name
			e.index = uint16(ri.Index())
			e.desc = x0
			r0 := ri.NextRxRef(&q.RxDmaRing, 0)
			copy(e.data[:], r0.DataSliceOffsetLen(0, 32))
			elog.Add(&e)
		}

		if f0, sw_owned = rx_dma_flags_x1(x0); !sw_owned {
			done = rx_done_found_hw_owned_descriptor
			break
		}

		b0 := uint(x0.n_bytes_this_descriptor)

		x0.refill(q.RefillRef(ri))

		q.rx_desc[i+0] = x0

		q.Rx1Descriptor(ri, b0, f0)

		n_left -= 1
		i += 1
		ri = ri.NextRingIndex(1)
	}

	if i >= reg(d.rx_ring_len) {
		i = 0
	}

	if done == rx_done_not_done && q.MaxNext() == vnet.MaxVectorLen {
		done = rx_done_vec_len
	}

	n_done += n_descriptors - n_left
	old_head := q.head_index
	q.head_index = i

	if elog.Enabled() {
		e := rx_no_wrap_elog{
			name:      d.elog_name,
			old_head:  old_head,
			new_head:  i,
			n_done:    n_done,
			done_code: done,
		}
		elog.Add(&e)
	}
	return
}

func (d *dev) rx_queue_interrupt(queue uint) {
	q := &d.rx_queues[queue]
	// Protects again polling and interrupt happening concurrently.
	q.mu.Lock()
	defer q.mu.Unlock()
	q.Out = d.out
	dr := q.get_regs()

	sw_head_index := q.head_index

	n_desc_done := reg(0)
	n_desc_end := q.len - sw_head_index
	// Do descriptors head to end of ring.
	done, n_desc_done := q.rx_no_wrap(n_desc_done, n_desc_end)
	if n_desc_done == n_desc_end {
		// Refill rx buffers when ring wraps.
		q.RxDmaRing.WrapRefill()
		// Do descriptors at start of ring.
		if sw_head_index > 0 && done != rx_done_found_hw_owned_descriptor {
			done, n_desc_done = q.rx_no_wrap(n_desc_done, sw_head_index)
		}
	}

	// Flush enqueue and counters.
	q.RxDmaRing.Flush()

	if n_desc_done > 0 {
		// Give tail back to hardware.
		hw.MemoryBarrier()

		q.tail_index += n_desc_done
		if q.tail_index > q.len {
			q.tail_index -= q.len
		}
		dr.tail_index.set(d, q.tail_index)
	}

	// Arrange to be called again if we've not processed all potential rx descriptors.
	if done != rx_done_found_hw_owned_descriptor {
		d.is_active += 1
	}

	if elog.Enabled() {
		e := rx_queue_elog{
			name:     d.elog_name,
			new_tail: q.tail_index,
		}
		elog.Add(&e)
	}
}

type rx_no_wrap_elog struct {
	name               elog.StringRef
	old_head, new_head reg
	n_done             reg
	done_code          rx_done_code
}

func (e *rx_no_wrap_elog) Elog(l *elog.Log) {
	l.Logf("%s rx head %d -> %d done %d %s", e.name, e.old_head, e.new_head, e.n_done, e.done_code)
}

type rx_queue_elog struct {
	name     elog.StringRef
	new_tail reg
}

func (e *rx_queue_elog) Elog(l *elog.Log) {
	l.Logf("%s rx tail to hw %d", e.name, e.new_tail)
}

type rx_desc_elog_kind uint16

const (
	rx_desc_elog_x1 = iota
	rx_desc_elog_x4
)

func (k rx_desc_elog_kind) String() string {
	t := [...]string{
		rx_desc_elog_x1: "x1",
		rx_desc_elog_x4: "x4",
	}
	return elib.StringerHex(t[:], int(k))
}

type rx_desc_elog struct {
	name  elog.StringRef
	kind  rx_desc_elog_kind
	index uint16
	desc  rx_from_hw_descriptor
	data  [24]byte
}

func (e *rx_desc_elog) Elog(l *elog.Log) {
	l.Logf("%s rx desc %v %d %v %x", e.name, e.kind, e.index, &e.desc, e.data[:])
}
