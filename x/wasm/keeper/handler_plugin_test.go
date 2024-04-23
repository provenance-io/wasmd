package keeper

import (
	"encoding/json"
	"testing"

	wasmvm "github.com/CosmWasm/wasmvm"
	wasmvmtypes "github.com/CosmWasm/wasmvm/types"
	"github.com/cosmos/gogoproto/proto"
	capabilitytypes "github.com/cosmos/ibc-go/modules/capability/types"
	clienttypes "github.com/cosmos/ibc-go/v8/modules/core/02-client/types" //nolint:staticcheck
	channeltypes "github.com/cosmos/ibc-go/v8/modules/core/04-channel/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	protov2 "google.golang.org/protobuf/proto"

	errorsmod "cosmossdk.io/errors"
	"cosmossdk.io/log"
	sdkmath "cosmossdk.io/math"

	"github.com/cosmos/cosmos-sdk/baseapp"
	"github.com/cosmos/cosmos-sdk/codec"
	sdk "github.com/cosmos/cosmos-sdk/types"
	sdkerrors "github.com/cosmos/cosmos-sdk/types/errors"
	banktypes "github.com/cosmos/cosmos-sdk/x/bank/types"

	"github.com/CosmWasm/wasmd/x/wasm/keeper/wasmtesting"
	"github.com/CosmWasm/wasmd/x/wasm/types"
)

func TestMessageHandlerChainDispatch(t *testing.T) {
	capturingHandler, gotMsgs := wasmtesting.NewCapturingMessageHandler()

	alwaysUnknownMsgHandler := &wasmtesting.MockMessageHandler{
		DispatchMsgFn: func(ctx sdk.Context, contractAddr sdk.AccAddress, contractIBCPortID string, msg wasmvmtypes.CosmosMsg) (events []sdk.Event, data [][]byte, err error) {
			return nil, nil, types.ErrUnknownMsg
		},
	}

	assertNotCalledHandler := &wasmtesting.MockMessageHandler{
		DispatchMsgFn: func(ctx sdk.Context, contractAddr sdk.AccAddress, contractIBCPortID string, msg wasmvmtypes.CosmosMsg) (events []sdk.Event, data [][]byte, err error) {
			t.Fatal("not expected to be called")
			return
		},
	}

	myMsg := wasmvmtypes.CosmosMsg{Custom: []byte(`{}`)}
	specs := map[string]struct {
		handlers  []Messenger
		expErr    *errorsmod.Error
		expEvents []sdk.Event
	}{
		"single handler": {
			handlers: []Messenger{capturingHandler},
		},
		"passed to next handler": {
			handlers: []Messenger{alwaysUnknownMsgHandler, capturingHandler},
		},
		"stops iteration when handled": {
			handlers: []Messenger{capturingHandler, assertNotCalledHandler},
		},
		"stops iteration on handler error": {
			handlers: []Messenger{&wasmtesting.MockMessageHandler{
				DispatchMsgFn: func(ctx sdk.Context, contractAddr sdk.AccAddress, contractIBCPortID string, msg wasmvmtypes.CosmosMsg) (events []sdk.Event, data [][]byte, err error) {
					return nil, nil, types.ErrInvalidMsg
				},
			}, assertNotCalledHandler},
			expErr: types.ErrInvalidMsg,
		},
		"return events when handle": {
			handlers: []Messenger{
				&wasmtesting.MockMessageHandler{
					DispatchMsgFn: func(ctx sdk.Context, contractAddr sdk.AccAddress, contractIBCPortID string, msg wasmvmtypes.CosmosMsg) (events []sdk.Event, data [][]byte, err error) {
						_, data, _ = capturingHandler.DispatchMsg(ctx, contractAddr, contractIBCPortID, msg)
						return []sdk.Event{sdk.NewEvent("myEvent", sdk.NewAttribute("foo", "bar"))}, data, nil
					},
				},
			},
			expEvents: []sdk.Event{sdk.NewEvent("myEvent", sdk.NewAttribute("foo", "bar"))},
		},
		"return error when none can handle": {
			handlers: []Messenger{alwaysUnknownMsgHandler},
			expErr:   types.ErrUnknownMsg,
		},
	}
	for name, spec := range specs {
		t.Run(name, func(t *testing.T) {
			*gotMsgs = make([]wasmvmtypes.CosmosMsg, 0)

			// when
			h := MessageHandlerChain{spec.handlers}
			gotEvents, gotData, gotErr := h.DispatchMsg(sdk.Context{}, RandomAccountAddress(t), "anyPort", myMsg)

			// then
			require.True(t, spec.expErr.Is(gotErr), "exp %v but got %#+v", spec.expErr, gotErr)
			if spec.expErr != nil {
				return
			}
			assert.Equal(t, []wasmvmtypes.CosmosMsg{myMsg}, *gotMsgs)
			assert.Equal(t, [][]byte{{1}}, gotData) // {1} is default in capturing handler
			assert.Equal(t, spec.expEvents, gotEvents)
		})
	}
}

func TestSDKMessageHandlerDispatch(t *testing.T) {
	myEvent := sdk.NewEvent("myEvent", sdk.NewAttribute("foo", "bar"))
	const myData = "myData"
	myRouterResult := sdk.Result{
		Data:   []byte(myData),
		Events: sdk.Events{myEvent}.ToABCIEvents(),
	}

	var gotMsg []sdk.Msg
	capturingMessageRouter := wasmtesting.MessageRouterFunc(func(msg sdk.Msg) baseapp.MsgServiceHandler {
		return func(ctx sdk.Context, req sdk.Msg) (*sdk.Result, error) {
			gotMsg = append(gotMsg, msg)
			return &myRouterResult, nil
		}
	})
	noRouteMessageRouter := wasmtesting.MessageRouterFunc(func(msg sdk.Msg) baseapp.MsgServiceHandler {
		return nil
	})
	myContractAddr := RandomAccountAddress(t)
	myContractMessage := wasmvmtypes.CosmosMsg{Custom: []byte("{}")}

	specs := map[string]struct {
		srcRoute         MessageRouter
		srcEncoder       CustomEncoder
		expErr           *errorsmod.Error
		expMsgDispatched int
		cdc              *MockCodec
	}{
		"all good": {
			srcRoute: capturingMessageRouter,
			srcEncoder: func(sender sdk.AccAddress, msg json.RawMessage) ([]sdk.Msg, error) {
				myMsg := types.MsgExecuteContract{
					Sender:   myContractAddr.String(),
					Contract: RandomBech32AccountAddress(t),
					Msg:      []byte("{}"),
				}
				return []sdk.Msg{&myMsg}, nil
			},
			expMsgDispatched: 1,
		},
		"handle multiple signers": {
			srcRoute: capturingMessageRouter,
			srcEncoder: func(sender sdk.AccAddress, msg json.RawMessage) ([]sdk.Msg, error) {
				myMsg := MockMessage{
					validateError: nil,
					signers:       []sdk.AccAddress{myContractAddr, sdk.MustAccAddressFromBech32(RandomBech32AccountAddress(t))},
				}
				return []sdk.Msg{&myMsg}, nil
			},
			expMsgDispatched: 1,
			cdc:              &MockCodec{},
		},
		"multiple output msgs": {
			srcRoute: capturingMessageRouter,
			srcEncoder: func(sender sdk.AccAddress, msg json.RawMessage) ([]sdk.Msg, error) {
				first := &types.MsgExecuteContract{
					Sender:   myContractAddr.String(),
					Contract: RandomBech32AccountAddress(t),
					Msg:      []byte("{}"),
				}
				second := &types.MsgExecuteContract{
					Sender:   myContractAddr.String(),
					Contract: RandomBech32AccountAddress(t),
					Msg:      []byte("{}"),
				}
				return []sdk.Msg{first, second}, nil
			},
			expMsgDispatched: 2,
		},
		"invalid sdk message rejected": {
			srcRoute: capturingMessageRouter,
			srcEncoder: func(sender sdk.AccAddress, msg json.RawMessage) ([]sdk.Msg, error) {
				invalidMsg := types.MsgExecuteContract{
					Sender:   myContractAddr.String(),
					Contract: RandomBech32AccountAddress(t),
					Msg:      []byte("INVALID_JSON"),
				}
				return []sdk.Msg{&invalidMsg}, nil
			},
			expErr: types.ErrInvalid,
		},
		"invalid sender rejected": {
			srcRoute: capturingMessageRouter,
			srcEncoder: func(sender sdk.AccAddress, msg json.RawMessage) ([]sdk.Msg, error) {
				invalidMsg := types.MsgExecuteContract{
					Sender:   RandomBech32AccountAddress(t),
					Contract: RandomBech32AccountAddress(t),
					Msg:      []byte("{}"),
				}
				return []sdk.Msg{&invalidMsg}, nil
			},
			expErr: sdkerrors.ErrUnauthorized,
		},
		"unroutable message rejected": {
			srcRoute: noRouteMessageRouter,
			srcEncoder: func(sender sdk.AccAddress, msg json.RawMessage) ([]sdk.Msg, error) {
				myMsg := types.MsgExecuteContract{
					Sender:   myContractAddr.String(),
					Contract: RandomBech32AccountAddress(t),
					Msg:      []byte("{}"),
				}
				return []sdk.Msg{&myMsg}, nil
			},
			expErr: sdkerrors.ErrUnknownRequest,
		},
		"encoding error passed": {
			srcRoute: capturingMessageRouter,
			srcEncoder: func(sender sdk.AccAddress, msg json.RawMessage) ([]sdk.Msg, error) {
				myErr := types.ErrUnpinContractFailed // any error that is not used
				return nil, myErr
			},
			expErr: types.ErrUnpinContractFailed,
		},
	}
	for name, spec := range specs {
		t.Run(name, func(t *testing.T) {
			gotMsg = make([]sdk.Msg, 0)

			// when
			ctx := sdk.Context{}
			cdc := MakeTestCodec(t)
			if spec.cdc != nil {
				cdc = spec.cdc.WithCodec(cdc)
			}
			h := NewSDKMessageHandler(cdc, spec.srcRoute, MessageEncoders{Custom: spec.srcEncoder})
			gotEvents, gotData, gotErr := h.DispatchMsg(ctx, myContractAddr, "myPort", myContractMessage)

			// then
			require.True(t, spec.expErr.Is(gotErr), "exp %v but got %#+v", spec.expErr, gotErr)
			if spec.expErr != nil {
				require.Len(t, gotMsg, 0)
				return
			}
			assert.Len(t, gotMsg, spec.expMsgDispatched)
			for i := 0; i < spec.expMsgDispatched; i++ {
				assert.Equal(t, myEvent, gotEvents[i])
				assert.Equal(t, []byte(myData), gotData[i])
			}
		})
	}
}

func TestIBCRawPacketHandler(t *testing.T) {
	ibcPort := "contractsIBCPort"
	ctx := sdk.Context{}.WithLogger(log.NewTestLogger(t))

	type CapturedPacket struct {
		sourcePort       string
		sourceChannel    string
		timeoutHeight    clienttypes.Height
		timeoutTimestamp uint64
		data             []byte
	}
	var capturedPacket *CapturedPacket

	capturePacketsSenderMock := &wasmtesting.MockIBCPacketSender{
		SendPacketFn: func(ctx sdk.Context, channelCap *capabilitytypes.Capability, sourcePort, sourceChannel string, timeoutHeight clienttypes.Height, timeoutTimestamp uint64, data []byte) (uint64, error) {
			capturedPacket = &CapturedPacket{
				sourcePort:       sourcePort,
				sourceChannel:    sourceChannel,
				timeoutHeight:    timeoutHeight,
				timeoutTimestamp: timeoutTimestamp,
				data:             data,
			}
			return 1, nil
		},
	}
	chanKeeper := &wasmtesting.MockChannelKeeper{
		GetChannelFn: func(ctx sdk.Context, srcPort, srcChan string) (channeltypes.Channel, bool) {
			return channeltypes.Channel{
				Counterparty: channeltypes.NewCounterparty(
					"other-port",
					"other-channel-1",
				),
			}, true
		},
	}
	capKeeper := &wasmtesting.MockCapabilityKeeper{
		GetCapabilityFn: func(ctx sdk.Context, name string) (*capabilitytypes.Capability, bool) {
			return &capabilitytypes.Capability{}, true
		},
	}

	specs := map[string]struct {
		srcMsg        wasmvmtypes.SendPacketMsg
		chanKeeper    types.ChannelKeeper
		capKeeper     types.CapabilityKeeper
		expPacketSent *CapturedPacket
		expErr        *errorsmod.Error
	}{
		"all good": {
			srcMsg: wasmvmtypes.SendPacketMsg{
				ChannelID: "channel-1",
				Data:      []byte("myData"),
				Timeout:   wasmvmtypes.IBCTimeout{Block: &wasmvmtypes.IBCTimeoutBlock{Revision: 1, Height: 2}},
			},
			chanKeeper: chanKeeper,
			capKeeper:  capKeeper,
			expPacketSent: &CapturedPacket{
				sourcePort:    ibcPort,
				sourceChannel: "channel-1",
				timeoutHeight: clienttypes.Height{RevisionNumber: 1, RevisionHeight: 2},
				data:          []byte("myData"),
			},
		},
		"capability not found returns error": {
			srcMsg: wasmvmtypes.SendPacketMsg{
				ChannelID: "channel-1",
				Data:      []byte("myData"),
				Timeout:   wasmvmtypes.IBCTimeout{Block: &wasmvmtypes.IBCTimeoutBlock{Revision: 1, Height: 2}},
			},
			chanKeeper: chanKeeper,
			capKeeper: wasmtesting.MockCapabilityKeeper{
				GetCapabilityFn: func(ctx sdk.Context, name string) (*capabilitytypes.Capability, bool) {
					return nil, false
				},
			},
			expErr: channeltypes.ErrChannelCapabilityNotFound,
		},
	}
	for name, spec := range specs {
		t.Run(name, func(t *testing.T) {
			capturedPacket = nil
			// when
			h := NewIBCRawPacketHandler(capturePacketsSenderMock, spec.chanKeeper, spec.capKeeper)
			evts, data, gotErr := h.DispatchMsg(ctx, RandomAccountAddress(t), ibcPort, wasmvmtypes.CosmosMsg{IBC: &wasmvmtypes.IBCMsg{SendPacket: &spec.srcMsg}}) //nolint:gosec
			// then
			require.True(t, spec.expErr.Is(gotErr), "exp %v but got %#+v", spec.expErr, gotErr)
			if spec.expErr != nil {
				return
			}

			assert.Nil(t, evts)
			require.NotNil(t, data)

			expMsg := types.MsgIBCSendResponse{Sequence: 1}

			actualMsg := types.MsgIBCSendResponse{}
			err := actualMsg.Unmarshal(data[0])
			require.NoError(t, err)

			assert.Equal(t, expMsg, actualMsg)
			assert.Equal(t, spec.expPacketSent, capturedPacket)
		})
	}
}

func TestBurnCoinMessageHandlerIntegration(t *testing.T) {
	// testing via full keeper setup so that we are confident the
	// module permissions are set correct and no other handler
	// picks the message in the default handler chain
	ctx, keepers := CreateDefaultTestInput(t)
	// set some supply
	keepers.Faucet.NewFundedRandomAccount(ctx, sdk.NewCoin("denom", sdkmath.NewInt(10_000_000)))
	k := keepers.WasmKeeper

	example := InstantiateHackatomExampleContract(t, ctx, keepers) // with deposit of 100 stake

	before, err := keepers.BankKeeper.TotalSupply(ctx, &banktypes.QueryTotalSupplyRequest{})
	require.NoError(t, err)

	specs := map[string]struct {
		msg    wasmvmtypes.BurnMsg
		expErr bool
	}{
		"all good": {
			msg: wasmvmtypes.BurnMsg{
				Amount: wasmvmtypes.Coins{{
					Denom:  "denom",
					Amount: "100",
				}},
			},
		},
		"not enough funds in contract": {
			msg: wasmvmtypes.BurnMsg{
				Amount: wasmvmtypes.Coins{{
					Denom:  "denom",
					Amount: "101",
				}},
			},
			expErr: true,
		},
		"zero amount rejected": {
			msg: wasmvmtypes.BurnMsg{
				Amount: wasmvmtypes.Coins{{
					Denom:  "denom",
					Amount: "0",
				}},
			},
			expErr: true,
		},
		"unknown denom - insufficient funds": {
			msg: wasmvmtypes.BurnMsg{
				Amount: wasmvmtypes.Coins{{
					Denom:  "unknown",
					Amount: "1",
				}},
			},
			expErr: true,
		},
	}
	parentCtx := ctx
	for name, spec := range specs {
		t.Run(name, func(t *testing.T) {
			ctx, _ = parentCtx.CacheContext()
			k.wasmVM = &wasmtesting.MockWasmEngine{ExecuteFn: func(codeID wasmvm.Checksum, env wasmvmtypes.Env, info wasmvmtypes.MessageInfo, executeMsg []byte, store wasmvm.KVStore, goapi wasmvm.GoAPI, querier wasmvm.Querier, gasMeter wasmvm.GasMeter, gasLimit uint64, deserCost wasmvmtypes.UFraction) (*wasmvmtypes.Response, uint64, error) {
				return &wasmvmtypes.Response{
					Messages: []wasmvmtypes.SubMsg{
						{Msg: wasmvmtypes.CosmosMsg{Bank: &wasmvmtypes.BankMsg{Burn: &spec.msg}}, ReplyOn: wasmvmtypes.ReplyNever}, //nolint:gosec
					},
				}, 0, nil
			}}

			// when
			_, err = k.execute(ctx, example.Contract, example.CreatorAddr, nil, nil)

			// then
			if spec.expErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)

			// and total supply reduced by burned amount
			after, err := keepers.BankKeeper.TotalSupply(ctx, &banktypes.QueryTotalSupplyRequest{})
			require.NoError(t, err)
			diff := before.Supply.Sub(after.Supply...)
			assert.Equal(t, sdk.NewCoins(sdk.NewCoin("denom", sdkmath.NewInt(100))), diff)
		})
	}

	// test cases:
	// not enough money to burn
}

type MockMessage struct {
	validateError error
	signers       []sdk.AccAddress
}

func (msg MockMessage) GetSigners() []sdk.AccAddress {
	return msg.signers
}

func (msg MockMessage) ValidateBasic() error {
	return msg.validateError
}

func (msg MockMessage) Reset() {
}

func (msg MockMessage) String() string {
	return ""
}

func (msg MockMessage) ProtoMessage() {
}

type MockCodec struct {
	codec.Codec
}

func (cdc *MockCodec) GetMsgV1Signers(msg proto.Message) ([][]byte, protov2.Message, error) {
	mock, ok := msg.(*MockMessage)
	if !ok {
		return cdc.Codec.GetMsgV1Signers(msg)
	}

	signers := mock.GetSigners()
	bytes := make([][]byte, 0, len(signers))
	for _, signer := range signers {
		bytes = append(bytes, signer.Bytes())
	}
	return bytes, nil, nil
}

func (cdc *MockCodec) WithCodec(codec codec.Codec) *MockCodec {
	cdc.Codec = codec
	return cdc
}
