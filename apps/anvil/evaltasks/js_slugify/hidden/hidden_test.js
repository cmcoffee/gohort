'use strict';
var assert = require('assert');
var slugify = require('./slug').slugify;
assert.strictEqual(slugify('Hello,  World!'), 'hello-world');
assert.strictEqual(slugify('  Two -- Dashes  '), 'two-dashes');
assert.strictEqual(slugify('abc123'), 'abc123');
console.log('ok');
