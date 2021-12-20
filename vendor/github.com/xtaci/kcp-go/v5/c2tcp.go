package kcp

import (
	"log"
	"math"
	"time"
)

const (
	IC2TCP_GOOD   uint32 = 0
	IC2TCP_NORMAL uint32 = 1
	IC2TCP_BAD    uint32 = 2

	IC2TCP_MAX_ALPHA float32 = 10.0
	IC2TCP_MIN_ALPHA float32 = 1.0
)

type c2tcp struct {
	c2tcp_enable     bool   // c2tcp enabled
	now              uint32 // current time of detector
	c2tcp_interval   uint32
	setpoint         uint32
	target           uint32
	alpha            float32
	alpha_upd_time   uint32
	min_rtt          uint32 // min_rtt
	min_rtt_upd_time uint32 // min_rtt update time
	rtt_sum          uint32 // total rtt
	rtt_cnt          uint32 // rtt update counter
	first_time       bool
	c2tcp_counter    uint32
	next_time        uint32
	condition        uint32
}

func (kcp *KCP) c2tcp_initial() {
	kcp.c2tcp_enable = false
	kcp.now = currentMs()
	kcp.c2tcp_interval = 0
	kcp.setpoint = 0
	kcp.alpha = 0
	kcp.target = 0
	kcp.alpha_upd_time = kcp.now
	kcp.min_rtt = 0
	kcp.min_rtt_upd_time = 0
	kcp.rtt_sum = 0
	kcp.rtt_cnt = 0
	kcp.first_time = false
	kcp.c2tcp_counter = 0
	kcp.next_time = 0
	kcp.condition = IC2TCP_NORMAL
}

// SetC2tcpPara set c2tcp para.
func (s *UDPSession) SetC2tcpPara(enable bool, alpha float32, target uint32) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.kcp.SetC2tcpPara(enable, alpha, target)
}

// SetC2tcpPara set c2tcp para.
func (kcp *KCP) SetC2tcpPara(enable bool, alpha float32, target uint32) int {
	kcp.c2tcp_enable = enable
	kcp.alpha = alpha
	kcp.target = target

	if kcp.alpha < IC2TCP_MIN_ALPHA {
		kcp.alpha = IC2TCP_MIN_ALPHA
	} else if kcp.alpha > IC2TCP_MAX_ALPHA {
		kcp.alpha = IC2TCP_MAX_ALPHA
	}
	return 0
}

func (kcp *KCP) c2tcp_tune_alpha(rtt uint32) {

	kcp.rtt_sum += uint32(rtt)
	kcp.rtt_cnt++
	// update alpha every 500ms
	if int64(_itimediff(kcp.now, kcp.alpha_upd_time)) < 500 {
		return
	}
	kcp.alpha_upd_time = kcp.now
	avg_rtt := kcp.rtt_sum / kcp.rtt_cnt
	if avg_rtt <= kcp.target {
		kcp.alpha += (float32(kcp.target) - float32(avg_rtt)) / (2 * float32(avg_rtt))
		if kcp.alpha > IC2TCP_MAX_ALPHA {
			kcp.alpha = IC2TCP_MAX_ALPHA
		}
	} else {
		kcp.alpha -= 2 * (float32(avg_rtt) - float32(kcp.target)) / float32(kcp.target)
		if kcp.alpha < IC2TCP_MIN_ALPHA {
			kcp.alpha = IC2TCP_MIN_ALPHA
		}
	}
	log.Println("c2tcp alpha set ", kcp.alpha)

	kcp.rtt_sum = rtt
	kcp.rtt_cnt = 1
}

func (kcp *KCP) c2tcp_upd_cwnd(rtt int32) {
	if kcp.condition == IC2TCP_NORMAL {
		return
	} else if kcp.condition == IC2TCP_GOOD {
		mss := kcp.mss
		kcp.incr = _imax_(kcp.incr, mss) + uint32(float32(mss)*float32(kcp.setpoint)/float32(kcp.cwnd*uint32(rtt)))

		if (kcp.cwnd+1)*mss <= kcp.incr {
			if mss > 0 {
				kcp.cwnd = (kcp.incr + mss - 1) / mss
			} else {
				kcp.cwnd = kcp.incr + mss - 1
			}
		}

		if kcp.cwnd > kcp.rmt_wnd {
			kcp.cwnd = kcp.rmt_wnd
			kcp.incr = kcp.rmt_wnd * mss
		}
	} else if kcp.condition == IC2TCP_BAD {
		/*
			kcp.ssthresh = kcp.cwnd / 2
			if kcp.ssthresh < IKCP_THRESH_MIN {
				kcp.ssthresh = IKCP_THRESH_MIN
			}
			kcp.cwnd = 1
			kcp.incr = kcp.mss
		*/

		inflight := kcp.snd_nxt - kcp.snd_una
		kcp.ssthresh = inflight / 2
		if kcp.ssthresh < IKCP_THRESH_MIN {
			kcp.ssthresh = IKCP_THRESH_MIN
		}
		kcp.cwnd = kcp.ssthresh
		kcp.incr = kcp.cwnd * kcp.mss
	}
}

func (kcp *KCP) c2tcp_detect_condition(rtt int32) {
	if !kcp.c2tcp_enable {
		return
	}

	kcp.now = currentMs()

	if kcp.min_rtt == 0 || kcp.min_rtt > uint32(rtt) || int64(_itimediff(kcp.now, kcp.min_rtt_upd_time)) >= time.Second.Milliseconds() {
		kcp.min_rtt = uint32(rtt)
		kcp.min_rtt_upd_time = kcp.now
	}

	kcp.c2tcp_tune_alpha(uint32(rtt))

	kcp.setpoint = uint32(float32(kcp.min_rtt)*kcp.alpha) + 1

	if rtt < int32(kcp.setpoint) {
		kcp.condition = IC2TCP_GOOD
		kcp.interval = kcp.setpoint
		kcp.first_time = true
		kcp.c2tcp_counter = 1
		kcp.c2tcp_upd_cwnd(rtt)
		log.Println("c2tcp GOOD condition detected, rtt:", rtt, ", target:", kcp.target, ", setpoint:", kcp.setpoint,
			", alpha: ", kcp.alpha, ", min_rtt: ", kcp.min_rtt, ", avg_rtt:", kcp.rtt_sum/kcp.rtt_cnt)
	} else if kcp.first_time {
		kcp.condition = IC2TCP_NORMAL
		kcp.first_time = false
		kcp.next_time = kcp.now + kcp.c2tcp_interval
		log.Println("c2tcp NORMAL condition detected, rtt:", rtt, ", target:", kcp.target, ", setpoint:", kcp.setpoint,
			", alpha: ", kcp.alpha, ", min_rtt: ", kcp.min_rtt, ", avg_rtt:", kcp.rtt_sum/kcp.rtt_cnt)
	} else if kcp.now > kcp.next_time {
		kcp.condition = IC2TCP_BAD
		kcp.next_time = kcp.now + _imax_(1, kcp.c2tcp_interval/uint32(math.Sqrt(float64(kcp.c2tcp_counter))))
		kcp.c2tcp_counter++
		kcp.c2tcp_upd_cwnd(rtt)
		log.Println("c2tcp BAD condition detected, rtt:", rtt, ", target:", kcp.target, ", setpoint:", kcp.setpoint,
			", alpha: ", kcp.alpha, ", min_rtt: ", kcp.min_rtt, ", avg_rtt:", kcp.rtt_sum/kcp.rtt_cnt)
	} else {
		log.Println("c2tcp condition not change, rtt:", rtt, ", target:", kcp.target, ", setpoint:", kcp.setpoint,
			", alpha: ", kcp.alpha, ", min_rtt: ", kcp.min_rtt, ", avg_rtt:", kcp.rtt_sum/kcp.rtt_cnt)
	}

}
