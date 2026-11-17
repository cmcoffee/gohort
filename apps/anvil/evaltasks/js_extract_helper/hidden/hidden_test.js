'use strict';
var assert = require('assert');
var f = require('./format');
assert.strictEqual(f.formatMoney(-7), '-0.07');
assert.strictEqual(f.formatMoney(100), '1.00');
assert.strictEqual(f.formatTime(0), '0:00');
assert.strictEqual(f.formatTime(600), '10:00');
console.log('ok');
