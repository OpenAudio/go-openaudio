// Code generated - DO NOT EDIT.
// This file is a generated binding and any manual changes will be lost.

package gen

import (
	"errors"
	"math/big"
	"strings"

	ethereum "github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/accounts/abi/bind"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/event"
)

// Reference imports to suppress errors if they are not otherwise used.
var (
	_ = errors.New
	_ = big.NewInt
	_ = strings.NewReader
	_ = ethereum.NotFound
	_ = bind.Bind
	_ = common.Big1
	_ = types.BloomLookup
	_ = event.NewSubscription
	_ = abi.ConvertType
)

// ProportionalRewardsMetaData contains all meta data concerning the ProportionalRewards contract.
var ProportionalRewardsMetaData = &bind.MetaData{
	ABI: "[{\"inputs\":[],\"stateMutability\":\"nonpayable\",\"type\":\"constructor\"},{\"inputs\":[],\"name\":\"getValue\",\"outputs\":[{\"internalType\":\"uint256\",\"name\":\"\",\"type\":\"uint256\"}],\"stateMutability\":\"view\",\"type\":\"function\"},{\"inputs\":[{\"internalType\":\"uint256\",\"name\":\"_value\",\"type\":\"uint256\"}],\"name\":\"setValue\",\"outputs\":[],\"stateMutability\":\"nonpayable\",\"type\":\"function\"},{\"inputs\":[],\"name\":\"value\",\"outputs\":[{\"internalType\":\"uint256\",\"name\":\"\",\"type\":\"uint256\"}],\"stateMutability\":\"view\",\"type\":\"function\"}]",
	Bin: "0x6080604052348015600e575f80fd5b5060015f81905550610171806100235f395ff3fe608060405234801561000f575f80fd5b506004361061003f575f3560e01c806320965255146100435780633fa4f24514610061578063552410771461007f575b5f80fd5b61004b61009b565b60405161005891906100c9565b60405180910390f35b6100696100a3565b60405161007691906100c9565b60405180910390f35b61009960048036038101906100949190610110565b6100a8565b005b5f8054905090565b5f5481565b805f8190555050565b5f819050919050565b6100c3816100b1565b82525050565b5f6020820190506100dc5f8301846100ba565b92915050565b5f80fd5b6100ef816100b1565b81146100f9575f80fd5b50565b5f8135905061010a816100e6565b92915050565b5f60208284031215610125576101246100e2565b5b5f610132848285016100fc565b9150509291505056fea264697066735822122081e585ff82b39c2efdef4c4b87a88673dad60dcc030df4bca5cb56ce5251d85964736f6c634300081a0033",
}

// ProportionalRewardsABI is the input ABI used to generate the binding from.
// Deprecated: Use ProportionalRewardsMetaData.ABI instead.
var ProportionalRewardsABI = ProportionalRewardsMetaData.ABI

// ProportionalRewardsBin is the compiled bytecode used for deploying new contracts.
// Deprecated: Use ProportionalRewardsMetaData.Bin instead.
var ProportionalRewardsBin = ProportionalRewardsMetaData.Bin

// DeployProportionalRewards deploys a new Ethereum contract, binding an instance of ProportionalRewards to it.
func DeployProportionalRewards(auth *bind.TransactOpts, backend bind.ContractBackend) (common.Address, *types.Transaction, *ProportionalRewards, error) {
	parsed, err := ProportionalRewardsMetaData.GetAbi()
	if err != nil {
		return common.Address{}, nil, nil, err
	}
	if parsed == nil {
		return common.Address{}, nil, nil, errors.New("GetABI returned nil")
	}

	address, tx, contract, err := bind.DeployContract(auth, *parsed, common.FromHex(ProportionalRewardsBin), backend)
	if err != nil {
		return common.Address{}, nil, nil, err
	}
	return address, tx, &ProportionalRewards{ProportionalRewardsCaller: ProportionalRewardsCaller{contract: contract}, ProportionalRewardsTransactor: ProportionalRewardsTransactor{contract: contract}, ProportionalRewardsFilterer: ProportionalRewardsFilterer{contract: contract}}, nil
}

// ProportionalRewards is an auto generated Go binding around an Ethereum contract.
type ProportionalRewards struct {
	ProportionalRewardsCaller     // Read-only binding to the contract
	ProportionalRewardsTransactor // Write-only binding to the contract
	ProportionalRewardsFilterer   // Log filterer for contract events
}

// ProportionalRewardsCaller is an auto generated read-only Go binding around an Ethereum contract.
type ProportionalRewardsCaller struct {
	contract *bind.BoundContract // Generic contract wrapper for the low level calls
}

// ProportionalRewardsTransactor is an auto generated write-only Go binding around an Ethereum contract.
type ProportionalRewardsTransactor struct {
	contract *bind.BoundContract // Generic contract wrapper for the low level calls
}

// ProportionalRewardsFilterer is an auto generated log filtering Go binding around an Ethereum contract events.
type ProportionalRewardsFilterer struct {
	contract *bind.BoundContract // Generic contract wrapper for the low level calls
}

// ProportionalRewardsSession is an auto generated Go binding around an Ethereum contract,
// with pre-set call and transact options.
type ProportionalRewardsSession struct {
	Contract     *ProportionalRewards // Generic contract binding to set the session for
	CallOpts     bind.CallOpts        // Call options to use throughout this session
	TransactOpts bind.TransactOpts    // Transaction auth options to use throughout this session
}

// ProportionalRewardsCallerSession is an auto generated read-only Go binding around an Ethereum contract,
// with pre-set call options.
type ProportionalRewardsCallerSession struct {
	Contract *ProportionalRewardsCaller // Generic contract caller binding to set the session for
	CallOpts bind.CallOpts              // Call options to use throughout this session
}

// ProportionalRewardsTransactorSession is an auto generated write-only Go binding around an Ethereum contract,
// with pre-set transact options.
type ProportionalRewardsTransactorSession struct {
	Contract     *ProportionalRewardsTransactor // Generic contract transactor binding to set the session for
	TransactOpts bind.TransactOpts              // Transaction auth options to use throughout this session
}

// ProportionalRewardsRaw is an auto generated low-level Go binding around an Ethereum contract.
type ProportionalRewardsRaw struct {
	Contract *ProportionalRewards // Generic contract binding to access the raw methods on
}

// ProportionalRewardsCallerRaw is an auto generated low-level read-only Go binding around an Ethereum contract.
type ProportionalRewardsCallerRaw struct {
	Contract *ProportionalRewardsCaller // Generic read-only contract binding to access the raw methods on
}

// ProportionalRewardsTransactorRaw is an auto generated low-level write-only Go binding around an Ethereum contract.
type ProportionalRewardsTransactorRaw struct {
	Contract *ProportionalRewardsTransactor // Generic write-only contract binding to access the raw methods on
}

// NewProportionalRewards creates a new instance of ProportionalRewards, bound to a specific deployed contract.
func NewProportionalRewards(address common.Address, backend bind.ContractBackend) (*ProportionalRewards, error) {
	contract, err := bindProportionalRewards(address, backend, backend, backend)
	if err != nil {
		return nil, err
	}
	return &ProportionalRewards{ProportionalRewardsCaller: ProportionalRewardsCaller{contract: contract}, ProportionalRewardsTransactor: ProportionalRewardsTransactor{contract: contract}, ProportionalRewardsFilterer: ProportionalRewardsFilterer{contract: contract}}, nil
}

// NewProportionalRewardsCaller creates a new read-only instance of ProportionalRewards, bound to a specific deployed contract.
func NewProportionalRewardsCaller(address common.Address, caller bind.ContractCaller) (*ProportionalRewardsCaller, error) {
	contract, err := bindProportionalRewards(address, caller, nil, nil)
	if err != nil {
		return nil, err
	}
	return &ProportionalRewardsCaller{contract: contract}, nil
}

// NewProportionalRewardsTransactor creates a new write-only instance of ProportionalRewards, bound to a specific deployed contract.
func NewProportionalRewardsTransactor(address common.Address, transactor bind.ContractTransactor) (*ProportionalRewardsTransactor, error) {
	contract, err := bindProportionalRewards(address, nil, transactor, nil)
	if err != nil {
		return nil, err
	}
	return &ProportionalRewardsTransactor{contract: contract}, nil
}

// NewProportionalRewardsFilterer creates a new log filterer instance of ProportionalRewards, bound to a specific deployed contract.
func NewProportionalRewardsFilterer(address common.Address, filterer bind.ContractFilterer) (*ProportionalRewardsFilterer, error) {
	contract, err := bindProportionalRewards(address, nil, nil, filterer)
	if err != nil {
		return nil, err
	}
	return &ProportionalRewardsFilterer{contract: contract}, nil
}

// bindProportionalRewards binds a generic wrapper to an already deployed contract.
func bindProportionalRewards(address common.Address, caller bind.ContractCaller, transactor bind.ContractTransactor, filterer bind.ContractFilterer) (*bind.BoundContract, error) {
	parsed, err := ProportionalRewardsMetaData.GetAbi()
	if err != nil {
		return nil, err
	}
	return bind.NewBoundContract(address, *parsed, caller, transactor, filterer), nil
}

// Call invokes the (constant) contract method with params as input values and
// sets the output to result. The result type might be a single field for simple
// returns, a slice of interfaces for anonymous returns and a struct for named
// returns.
func (_ProportionalRewards *ProportionalRewardsRaw) Call(opts *bind.CallOpts, result *[]interface{}, method string, params ...interface{}) error {
	return _ProportionalRewards.Contract.ProportionalRewardsCaller.contract.Call(opts, result, method, params...)
}

// Transfer initiates a plain transaction to move funds to the contract, calling
// its default method if one is available.
func (_ProportionalRewards *ProportionalRewardsRaw) Transfer(opts *bind.TransactOpts) (*types.Transaction, error) {
	return _ProportionalRewards.Contract.ProportionalRewardsTransactor.contract.Transfer(opts)
}

// Transact invokes the (paid) contract method with params as input values.
func (_ProportionalRewards *ProportionalRewardsRaw) Transact(opts *bind.TransactOpts, method string, params ...interface{}) (*types.Transaction, error) {
	return _ProportionalRewards.Contract.ProportionalRewardsTransactor.contract.Transact(opts, method, params...)
}

// Call invokes the (constant) contract method with params as input values and
// sets the output to result. The result type might be a single field for simple
// returns, a slice of interfaces for anonymous returns and a struct for named
// returns.
func (_ProportionalRewards *ProportionalRewardsCallerRaw) Call(opts *bind.CallOpts, result *[]interface{}, method string, params ...interface{}) error {
	return _ProportionalRewards.Contract.contract.Call(opts, result, method, params...)
}

// Transfer initiates a plain transaction to move funds to the contract, calling
// its default method if one is available.
func (_ProportionalRewards *ProportionalRewardsTransactorRaw) Transfer(opts *bind.TransactOpts) (*types.Transaction, error) {
	return _ProportionalRewards.Contract.contract.Transfer(opts)
}

// Transact invokes the (paid) contract method with params as input values.
func (_ProportionalRewards *ProportionalRewardsTransactorRaw) Transact(opts *bind.TransactOpts, method string, params ...interface{}) (*types.Transaction, error) {
	return _ProportionalRewards.Contract.contract.Transact(opts, method, params...)
}

// GetValue is a free data retrieval call binding the contract method 0x20965255.
//
// Solidity: function getValue() view returns(uint256)
func (_ProportionalRewards *ProportionalRewardsCaller) GetValue(opts *bind.CallOpts) (*big.Int, error) {
	var out []interface{}
	err := _ProportionalRewards.contract.Call(opts, &out, "getValue")

	if err != nil {
		return *new(*big.Int), err
	}

	out0 := *abi.ConvertType(out[0], new(*big.Int)).(**big.Int)

	return out0, err

}

// GetValue is a free data retrieval call binding the contract method 0x20965255.
//
// Solidity: function getValue() view returns(uint256)
func (_ProportionalRewards *ProportionalRewardsSession) GetValue() (*big.Int, error) {
	return _ProportionalRewards.Contract.GetValue(&_ProportionalRewards.CallOpts)
}

// GetValue is a free data retrieval call binding the contract method 0x20965255.
//
// Solidity: function getValue() view returns(uint256)
func (_ProportionalRewards *ProportionalRewardsCallerSession) GetValue() (*big.Int, error) {
	return _ProportionalRewards.Contract.GetValue(&_ProportionalRewards.CallOpts)
}

// Value is a free data retrieval call binding the contract method 0x3fa4f245.
//
// Solidity: function value() view returns(uint256)
func (_ProportionalRewards *ProportionalRewardsCaller) Value(opts *bind.CallOpts) (*big.Int, error) {
	var out []interface{}
	err := _ProportionalRewards.contract.Call(opts, &out, "value")

	if err != nil {
		return *new(*big.Int), err
	}

	out0 := *abi.ConvertType(out[0], new(*big.Int)).(**big.Int)

	return out0, err

}

// Value is a free data retrieval call binding the contract method 0x3fa4f245.
//
// Solidity: function value() view returns(uint256)
func (_ProportionalRewards *ProportionalRewardsSession) Value() (*big.Int, error) {
	return _ProportionalRewards.Contract.Value(&_ProportionalRewards.CallOpts)
}

// Value is a free data retrieval call binding the contract method 0x3fa4f245.
//
// Solidity: function value() view returns(uint256)
func (_ProportionalRewards *ProportionalRewardsCallerSession) Value() (*big.Int, error) {
	return _ProportionalRewards.Contract.Value(&_ProportionalRewards.CallOpts)
}

// SetValue is a paid mutator transaction binding the contract method 0x55241077.
//
// Solidity: function setValue(uint256 _value) returns()
func (_ProportionalRewards *ProportionalRewardsTransactor) SetValue(opts *bind.TransactOpts, _value *big.Int) (*types.Transaction, error) {
	return _ProportionalRewards.contract.Transact(opts, "setValue", _value)
}

// SetValue is a paid mutator transaction binding the contract method 0x55241077.
//
// Solidity: function setValue(uint256 _value) returns()
func (_ProportionalRewards *ProportionalRewardsSession) SetValue(_value *big.Int) (*types.Transaction, error) {
	return _ProportionalRewards.Contract.SetValue(&_ProportionalRewards.TransactOpts, _value)
}

// SetValue is a paid mutator transaction binding the contract method 0x55241077.
//
// Solidity: function setValue(uint256 _value) returns()
func (_ProportionalRewards *ProportionalRewardsTransactorSession) SetValue(_value *big.Int) (*types.Transaction, error) {
	return _ProportionalRewards.Contract.SetValue(&_ProportionalRewards.TransactOpts, _value)
}
