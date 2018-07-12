package tasks

import (
	bosherr "github.com/cloudfoundry/bosh-utils/errors"
	boshlog "github.com/cloudfoundry/bosh-utils/logger"
	boshsys "github.com/cloudfoundry/bosh-utils/system"
)

// See http://www.linuxfoundation.org/collaborate/workgroups/networking/netem
// See http://mark.koli.ch/slowdown-throttle-bandwidth-linux-network-interface
type ControlNetOptions struct {
	Type    string
	Timeout string // Times may be suffixed with ms,s,m,h

	// slow: tc qdisc add dev eth0 root netem delay 50ms 10ms distribution normal
	Delay          string
	DelayVariation string

	// flaky: tc qdisc add dev eth0 root netem loss 20% 75%
	Loss            string
	LossCorrelation string

	// tc qdisc add dev eth0 root netem duplicate 1%
	Duplication string

	// tc qdisc add dev eth0 root netem corrupt 0.1%
	Corruption string

	// tc qdisc add dec eth0 root netem reorder 25% 50%
	Reorder            string
	ReorderCorrelation string

	// tc qdisc add dev eth0 root handle 1:0 [netem...]
	// tc qdisc add dev eth0 parent 1:1 handle 10: tfb rate 256kbit buffer 1600 limit 3000
	Bandwidth string

	// reset: tc qdisc del dev eth0 root
}

func (ControlNetOptions) _private() {}

type ControlNetTask struct {
	cmdRunner boshsys.CmdRunner
	opts      ControlNetOptions
}

func NewControlNetTask(cmdRunner boshsys.CmdRunner, opts ControlNetOptions, _ boshlog.Logger) ControlNetTask {
	return ControlNetTask{cmdRunner, opts}
}

func defaultStr(v, d string) string {
	if len(v) == 0 {
		return d
	} else {
		return v
	}
}

func (t ControlNetTask) Execute(stopCh chan struct{}) error {
	timeoutCh, err := NewOptionalTimeoutCh(t.opts.Timeout)
	if err != nil {
		return err
	}

	delay, loss, duplication, corruption, reorder, bandwidth := len(t.opts.Delay) > 0, len(t.opts.Loss) > 0, len(t.opts.Duplication) > 0, len(t.opts.Corruption) > 0, len(t.opts.Reorder) > 0, len(t.opts.Bandwidth) > 0

	if !(delay || loss || duplication || corruption || reorder || bandwidth) {
		return bosherr.Error("Must specify an effect")
	}

	if bandwidth && (delay || loss || duplication || corruption || reorder) {
		return bosherr.Error("Cannot limit the bandwidth at the same time as other effects")
	}

	ifaceNames, err := NonLocalIfaceNames()
	if err != nil {
		return err
	}

	if bandwidth {
		for _, ifaceName := range ifaceNames {
			err := t.configureBandwidth(ifaceName)
			if err != nil {
				return err
			}
		}
	} else {
		opts := make([]string, 0, 16)

		if delay {
			variation := defaultStr(t.opts.DelayVariation, "10ms")
			opts = append(opts, "delay", t.opts.Delay, variation, "distribution", "normal")
		}

		if loss {
			correlation := defaultStr(t.opts.LossCorrelation, "75%")
			opts = append(opts, "loss", t.opts.Loss, correlation)
		}

		if duplication {
			opts = append(opts, "duplicate", t.opts.Duplication)
		}

		if corruption {
			opts = append(opts, "corrupt", t.opts.Corruption)
		}

		if reorder {
			correlation := defaultStr(t.opts.ReorderCorrelation, "50%")
			opts = append(opts, "reorder", t.opts.Reorder, correlation)
		}

		for _, ifaceName := range ifaceNames {
			err := t.configureInterface(ifaceName, opts)
			if err != nil {
				return err
			}
		}
	}

	select {
	case <-timeoutCh:
	case <-stopCh:
	}

	for _, ifaceName := range ifaceNames {
		err := t.resetIface(ifaceName)
		if err != nil {
			return err
		}
	}

	return nil
}

func (t ControlNetTask) configureInterface(ifaceName string, opts []string) error {
	args := []string{"qdisc", "add", "dev", ifaceName, "root", "netem"}
	args = append(args, opts...)

	_, _, _, err := t.cmdRunner.RunCommand("tc", args...)
	if err != nil {
		return bosherr.WrapError(err, "Shelling out to tc")
	}

	return nil
}

func (t ControlNetTask) configureBandwidth(ifaceName string) error {
	_, _, _, err := t.cmdRunner.RunCommand("tc", "qdisc", "add", "dev", ifaceName, "handle", "1:", "root", "htb", "default", "11")
	if err != nil {
		return err
	}

	_, _, _, err = t.cmdRunner.RunCommand("tc", "class", "add", "dev", ifaceName, "parent", "1:", "classid", "1:1", "htb", "rate", t.opts.Bandwidth)
	if err != nil {
		t.resetIface(ifaceName)
		return err
	}

	_, _, _, err = t.cmdRunner.RunCommand("tc", "class", "add", "dev", ifaceName, "parent", "1:1", "classid", "1:11", "htb", "rate", t.opts.Bandwidth)
	if err != nil {
		t.resetIface(ifaceName)
		return err
	}

	return nil
}

func (t ControlNetTask) resetIface(ifaceName string) error {
	_, _, _, err := t.cmdRunner.RunCommand("tc", "qdisc", "del", "dev", ifaceName, "root")
	if err != nil {
		return bosherr.WrapError(err, "Resetting tc")
	}

	return nil
}
