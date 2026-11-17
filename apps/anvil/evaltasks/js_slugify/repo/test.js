'use strict';
var assert = require('assert');
var slugify = require('./slug').slugify;
assert.strictEqual(slugify('Hello'), 'hello');
console.log('ok');
