package main

import (
	"encoding/binary"
	KCP "github.com/xtaci/kcp-go/v5"
	"io"
	"net"
	"os"
	"strconv"
	"time"
)

type kcp_proto struct {
}

func (kcp *kcp_proto) name() string {
	return KCP_NAME
}

func (kcp *kcp_proto) accept(test *iperf_test) (net.Conn, error) {
	log.Debugf("Enter KCP accept")
	conn, err := test.proto_listener.Accept()
	if err != nil {
		return nil, err
	}
	buf := make([]byte, 4)
	n, err := conn.Read(buf)
	signal := binary.LittleEndian.Uint32(buf[:])
	if err != nil || n != 4 || signal != ACCEPT_SIGNAL {
		log.Errorf("KCP Receive Unexpected signal")
	}
	log.Debugf("KCP accept succeed. signal = %v", signal)
	return conn, nil
}

func (kcp *kcp_proto) listen(test *iperf_test) (net.Listener, error) {
	listener, err := KCP.ListenWithOptions(":"+strconv.Itoa(int(test.port)), nil, int(test.setting.data_shards), int(test.setting.parity_shards))
	listener.SetReadBuffer(int(test.setting.read_buf_size)) // all income conn share the same underline packet conn, the buffer should be large
	listener.SetWriteBuffer(int(test.setting.write_buf_size))

	if err != nil {
		return nil, err
	}
	return listener, nil
}

func (kcp *kcp_proto) connect(test *iperf_test) (net.Conn, error) {
	conn, err := KCP.DialWithOptions(test.addr+":"+strconv.Itoa(int(test.port)), nil, int(test.setting.data_shards), int(test.setting.parity_shards))
	if err != nil {
		return nil, err
	}
	buf := make([]byte, 4)
	binary.LittleEndian.PutUint32(buf, ACCEPT_SIGNAL)
	n, err := conn.Write(buf)
	if err != nil || n != 4 {
		log.Errorf("KCP send accept signal failed")
	}
	log.Debugf("KCP connect succeed.")
	return conn, nil
}

func (kcp *kcp_proto) send(sp *iperf_stream) int {
	n, err := sp.conn.(*KCP.UDPSession).Write(sp.buffer)
	if err != nil {
		if serr, ok := err.(*net.OpError); ok {
			log.Debugf("kcp conn already close = %v", serr)
			return -1
		} else if err.Error() == "broken pipe" {
			log.Debugf("kcp conn already close = %v", err.Error())
			return -1
		} else if err == os.ErrClosed || err == io.ErrClosedPipe {
			log.Debugf("send kcp socket close.")
			return -1
		}
		log.Errorf("kcp write err = %T %v", err, err)
		return -2
	}
	if n < 0 {
		log.Errorf("kcp write err. n = %v", n)
		return n
	}
	sp.result.bytes_sent += uint64(n)
	sp.result.bytes_sent_this_interval += uint64(n)
	//log.Debugf("KCP send %v bytes of total %v", n, sp.result.bytes_sent)
	return n
}

func (kcp *kcp_proto) recv(sp *iperf_stream) int {
	// recv is blocking
	n, err := sp.conn.(*KCP.UDPSession).Read(sp.buffer)

	if err != nil {
		if serr, ok := err.(*net.OpError); ok {
			log.Debugf("kcp conn already close = %v", serr)
			return -1
		} else if err.Error() == "broken pipe" {
			log.Debugf("kcp conn already close = %v", err.Error())
			return -1
		} else if err == io.EOF || err == os.ErrClosed || err == io.ErrClosedPipe {
			log.Debugf("recv kcp socket close. EOF")
			return -1
		}
		log.Errorf("kcp recv err = %T %v", err, err)
		return -2
	}
	if n < 0 {
		return n
	}
	if sp.test.state == TEST_RUNNING {
		sp.result.bytes_received += uint64(n)
		sp.result.bytes_received_this_interval += uint64(n)
	}
	//log.Debugf("KCP recv %v bytes of total %v", n, sp.result.bytes_received)
	return n
}

func (kcp *kcp_proto) init(test *iperf_test) int {
	for _, sp := range test.streams {
		sp.conn.(*KCP.UDPSession).SetReadBuffer(int(test.setting.read_buf_size))
		sp.conn.(*KCP.UDPSession).SetWriteBuffer(int(test.setting.write_buf_size))
		sp.conn.(*KCP.UDPSession).SetWindowSize(int(test.setting.snd_wnd), int(test.setting.rcv_wnd))
		sp.conn.(*KCP.UDPSession).SetStreamMode(true)
		sp.conn.(*KCP.UDPSession).SetDSCP(46)
		sp.conn.(*KCP.UDPSession).SetMtu(1400)
		sp.conn.(*KCP.UDPSession).SetACKNoDelay(false)
		sp.conn.(*KCP.UDPSession).SetDeadline(time.Now().Add(time.Minute))
		var no_delay, resend, nc int
		if test.no_delay {
			no_delay = 1
		} else {
			no_delay = 0
		}
		if test.setting.no_cong {
			nc = 1
		} else {
			nc = 0
		}
		resend = int(test.setting.fast_resend)
		sp.conn.(*KCP.UDPSession).SetNoDelay(no_delay, int(test.setting.flush_interval), resend, nc)
	}
	return 0
}

func (kcp *kcp_proto) stats_callback(test *iperf_test, sp *iperf_stream, temp_result *iperf_interval_results) int {
	rp := sp.result
	total_retrans := uint(KCP.DefaultSnmp.RetransSegs)
	total_lost := uint(KCP.DefaultSnmp.LostSegs)
	total_early_retrans := uint(KCP.DefaultSnmp.EarlyRetransSegs)
	total_fast_retrans := uint(KCP.DefaultSnmp.FastRetransSegs)
	total_recovers := uint(KCP.DefaultSnmp.FECRecovered)
	total_in_pkts := uint(KCP.DefaultSnmp.InPkts)
	total_in_segs := uint(KCP.DefaultSnmp.InSegs)
	total_out_pkts := uint(KCP.DefaultSnmp.OutPkts)
	total_out_segs := uint(KCP.DefaultSnmp.OutSegs)
	// retrans
	temp_result.interval_retrans = total_retrans - rp.stream_prev_total_retrans
	rp.stream_retrans += temp_result.interval_retrans
	rp.stream_prev_total_retrans = total_retrans
	// lost
	temp_result.interval_lost = total_lost - rp.stream_prev_total_lost
	rp.stream_lost += temp_result.interval_lost
	rp.stream_prev_total_lost = total_lost
	// early retrans
	temp_result.interval_early_retrans = total_early_retrans - rp.stream_prev_total_early_retrans
	rp.stream_early_retrans += temp_result.interval_early_retrans
	rp.stream_prev_total_early_retrans = total_early_retrans
	// fast retrans
	temp_result.interval_fast_retrans = total_fast_retrans - rp.stream_prev_total_fast_retrans
	rp.stream_fast_retrans += temp_result.interval_fast_retrans
	rp.stream_prev_total_fast_retrans = total_fast_retrans
	// recover
	rp.stream_recovers = total_recovers
	// packets receive
	rp.stream_in_pkts = total_in_pkts
	rp.stream_out_pkts = total_out_pkts
	// segs receive
	rp.stream_in_segs = total_in_segs
	rp.stream_out_segs = total_out_segs

	temp_result.rtt = uint(sp.conn.(*KCP.UDPSession).GetSRTTVar() * 1000) // ms to micro sec
	if rp.stream_min_rtt == 0 || temp_result.rtt < rp.stream_min_rtt {
		rp.stream_min_rtt = temp_result.rtt
	}
	if rp.stream_max_rtt == 0 || temp_result.rtt > rp.stream_max_rtt {
		rp.stream_max_rtt = temp_result.rtt
	}
	rp.stream_sum_rtt += temp_result.rtt
	rp.stream_cnt_rtt++
	return 0
}

func (kcp *kcp_proto) teardown(test *iperf_test) int {
	return 0
}
