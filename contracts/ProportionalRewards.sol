// SPDX-License-Identifier: MIT
pragma solidity ^0.8.0;

contract ProportionalRewards {
    uint256 public value;

    constructor() {
        value = 1;
    }

    function getValue() public view returns (uint256) {
        return value;
    }

    function setValue(uint256 _value) public {
        value = _value;
    }
}
