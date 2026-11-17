'use strict';
var assert = require('assert');
var f = require('./format');
assert.strictEqual(f.formatMoney(1205), '12.05');
assert.strictEqual(f.formatTime(65), '1:05');
console.log('ok');
