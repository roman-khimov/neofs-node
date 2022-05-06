package morph

import (
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"text/tabwriter"

	"github.com/nspcc-dev/neo-go/pkg/io"
	"github.com/nspcc-dev/neo-go/pkg/smartcontract/callflag"
	"github.com/nspcc-dev/neo-go/pkg/util"
	"github.com/nspcc-dev/neo-go/pkg/vm"
	"github.com/nspcc-dev/neo-go/pkg/vm/emit"
	"github.com/nspcc-dev/neo-go/pkg/vm/stackitem"
	"github.com/nspcc-dev/neofs-contract/nns"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

const lastGlagoliticLetter = 41

const forceConfigSet = "force"

type contractDumpInfo struct {
	hash    util.Uint160
	name    string
	version string
}

func dumpContractHashes(cmd *cobra.Command, _ []string) error {
	c, err := getN3Client(viper.GetViper())
	if err != nil {
		return fmt.Errorf("can't create N3 client: %w", err)
	}

	cs, err := c.GetContractStateByID(1)
	if err != nil {
		return err
	}

	infos := []contractDumpInfo{{name: nnsContract, hash: cs.Hash}}

	irSize := 0
	for ; irSize < lastGlagoliticLetter; irSize++ {
		ok, err := nnsIsAvailable(c, cs.Hash, getAlphabetNNSDomain(irSize))
		if err != nil {
			return err
		} else if ok {
			break
		}
	}

	buf := bytes.NewBuffer(nil)
	bw := io.NewBufBinWriter()

	if irSize != 0 {
		bw.Reset()
		for i := 0; i < irSize; i++ {
			emit.AppCall(bw.BinWriter, cs.Hash, "resolve", callflag.ReadOnly,
				getAlphabetNNSDomain(i),
				int64(nns.TXT))
		}

		alphaRes, err := c.InvokeScript(bw.Bytes(), nil)
		if err != nil {
			return fmt.Errorf("can't fetch info from NNS: %w", err)
		}

		for i := 0; i < irSize; i++ {
			info := contractDumpInfo{name: fmt.Sprintf("alphabet %d", i)}
			if h, err := parseNNSResolveResult(alphaRes.Stack[i]); err == nil {
				info.hash = h
			}
			infos = append(infos, info)
		}
	}

	for _, ctrName := range contractList {
		bw.Reset()
		emit.AppCall(bw.BinWriter, cs.Hash, "resolve", callflag.ReadOnly,
			ctrName+".neofs", int64(nns.TXT))

		res, err := c.InvokeScript(bw.Bytes(), nil)
		if err != nil {
			return fmt.Errorf("can't fetch info from NNS: %w", err)
		}

		info := contractDumpInfo{name: ctrName}
		if len(res.Stack) != 0 {
			if h, err := parseNNSResolveResult(res.Stack[0]); err == nil {
				info.hash = h
			}
		}
		infos = append(infos, info)
	}

	bw.Reset()
	for i := range infos {
		if infos[i].hash.Equals(util.Uint160{}) {
			emit.Int(bw.BinWriter, 0)
		} else {
			emit.AppCall(bw.BinWriter, infos[i].hash, "version", callflag.NoneFlag)
		}
	}

	res, err := c.InvokeScript(bw.Bytes(), nil)
	if err != nil {
		return fmt.Errorf("can't fetch info from NNS: %w", err)
	}

	if res.State == vm.HaltState.String() {
		for i := range res.Stack {
			infos[i].version = parseContractVersion(res.Stack[i])
		}
	}

	tw := tabwriter.NewWriter(buf, 0, 2, 2, ' ', 0)
	for _, info := range infos {
		if info.version == "" {
			info.version = "unknown"
		}
		_, _ = tw.Write([]byte(fmt.Sprintf("%s\t(%s):\t%s\n",
			info.name, info.version, info.hash.StringLE())))
	}
	_ = tw.Flush()

	cmd.Print(buf.String())

	return nil
}

func parseContractVersion(item stackitem.Item) string {
	bi, err := item.TryInteger()
	if err != nil || bi.Sign() == 0 || !bi.IsInt64() {
		return "unknown"
	}

	v := bi.Int64()
	major := v / 1_000_000
	minor := (v % 1_000_000) / 1000
	patch := v % 1_000
	return fmt.Sprintf("v%d.%d.%d", major, minor, patch)
}

func dumpNetworkConfig(cmd *cobra.Command, _ []string) error {
	c, err := getN3Client(viper.GetViper())
	if err != nil {
		return fmt.Errorf("can't create N3 client: %w", err)
	}

	cs, err := c.GetContractStateByID(1)
	if err != nil {
		return fmt.Errorf("can't get NNS contract info: %w", err)
	}

	nmHash, err := nnsResolveHash(c, cs.Hash, netmapContract+".neofs")
	if err != nil {
		return fmt.Errorf("can't get netmap contract hash: %w", err)
	}

	res, err := invokeFunction(c, nmHash, "listConfig", nil, nil)
	if err != nil || res.State != vm.HaltState.String() || len(res.Stack) == 0 {
		return errors.New("can't fetch list of network config keys from the netmap contract")
	}

	arr, ok := res.Stack[0].Value().([]stackitem.Item)
	if !ok {
		return errors.New("invalid ListConfig response from netmap contract")
	}

	buf := bytes.NewBuffer(nil)
	tw := tabwriter.NewWriter(buf, 0, 2, 2, ' ', 0)

	for _, param := range arr {
		tuple, ok := param.Value().([]stackitem.Item)
		if !ok || len(tuple) != 2 {
			return errors.New("invalid ListConfig response from netmap contract")
		}

		k, err := tuple[0].TryBytes()
		if err != nil {
			return errors.New("invalid config key from netmap contract")
		}

		v, err := tuple[1].TryBytes()
		if err != nil {
			return invalidConfigValueErr(k)
		}

		switch string(k) {
		case netmapAuditFeeKey, netmapBasicIncomeRateKey,
			netmapContainerFeeKey, netmapContainerAliasFeeKey,
			netmapEigenTrustIterationsKey,
			netmapEpochKey, netmapInnerRingCandidateFeeKey,
			netmapMaxObjectSizeKey, netmapWithdrawFeeKey:
			nbuf := make([]byte, 8)
			copy(nbuf[:], v)
			n := binary.LittleEndian.Uint64(nbuf)
			_, _ = tw.Write([]byte(fmt.Sprintf("%s:\t%d (int)\n", k, n)))
		case netmapEigenTrustAlphaKey:
			_, _ = tw.Write([]byte(fmt.Sprintf("%s:\t%s (str)\n", k, v)))
		case netmapHomomorphicHashDisabledKey:
			vBool, err := tuple[1].TryBool()
			if err != nil {
				return invalidConfigValueErr(k)
			}

			_, _ = tw.Write([]byte(fmt.Sprintf("%s:\t%t (bool)\n", k, vBool)))
		default:
			_, _ = tw.Write([]byte(fmt.Sprintf("%s:\t%s (hex)\n", k, hex.EncodeToString(v))))
		}
	}

	_ = tw.Flush()
	cmd.Print(buf.String())

	return nil
}

func setConfigCmd(cmd *cobra.Command, args []string) error {
	if len(args) == 0 {
		return errors.New("empty config pairs")
	}

	wCtx, err := newInitializeContext(cmd, viper.GetViper())
	if err != nil {
		return fmt.Errorf("can't initialize context: %w", err)
	}

	cs, err := wCtx.Client.GetContractStateByID(1)
	if err != nil {
		return fmt.Errorf("can't get NNS contract info: %w", err)
	}

	nmHash, err := nnsResolveHash(wCtx.Client, cs.Hash, netmapContract+".neofs")
	if err != nil {
		return fmt.Errorf("can't get netmap contract hash: %w", err)
	}

	forceFlag, _ := cmd.Flags().GetBool(forceConfigSet)

	bw := io.NewBufBinWriter()
	for _, arg := range args {
		k, v, err := parseConfigPair(arg, forceFlag)
		if err != nil {
			return err
		}

		// In NeoFS this is done via Notary contract. Here, however, we can form the
		// transaction locally. The first `nil` argument is required only for notary
		// disabled environment which is not supported by that command.
		emit.AppCall(bw.BinWriter, nmHash, "setConfig", callflag.All, nil, k, v)
		if bw.Err != nil {
			return fmt.Errorf("can't form raw transaction: %w", bw.Err)
		}
	}

	err = wCtx.sendCommitteeTx(bw.Bytes(), -1, true)
	if err != nil {
		return err
	}

	return wCtx.awaitTx()
}

func parseConfigPair(kvStr string, force bool) (key string, val interface{}, err error) {
	kv := strings.SplitN(kvStr, "=", 2)
	if len(kv) != 2 {
		return "", nil, fmt.Errorf("invalid parameter format: must be 'key=val', got: %s", kvStr)
	}

	key = kv[0]
	valRaw := kv[1]

	switch key {
	case netmapAuditFeeKey, netmapBasicIncomeRateKey,
		netmapContainerFeeKey, netmapContainerAliasFeeKey,
		netmapEigenTrustIterationsKey,
		netmapEpochKey, netmapInnerRingCandidateFeeKey,
		netmapMaxObjectSizeKey, netmapWithdrawFeeKey:
		val, err = strconv.ParseInt(valRaw, 10, 64)
		if err != nil {
			err = fmt.Errorf("could not parse %s's value '%s' as int: %w", key, valRaw, err)
		}
	case netmapEigenTrustAlphaKey:
		// just check that it could
		// be parsed correctly
		_, err = strconv.ParseFloat(kv[1], 64)
		if err != nil {
			err = fmt.Errorf("could not parse %s's value '%s' as float: %w", key, valRaw, err)
		}

		val = valRaw
	case netmapHomomorphicHashDisabledKey:
		val, err = strconv.ParseBool(valRaw)
		if err != nil {
			err = fmt.Errorf("could not parse %s's value '%s' as bool: %w", key, valRaw, err)
		}

	default:
		if !force {
			return "", nil, fmt.Errorf(
				"'%s' key is not well-known, use '--%s' flag if want to set it anyway",
				key, forceConfigSet)
		}

		val = valRaw
	}

	return
}

func invalidConfigValueErr(key []byte) error {
	return fmt.Errorf("invalid %s config value from netmap contract", key)
}
