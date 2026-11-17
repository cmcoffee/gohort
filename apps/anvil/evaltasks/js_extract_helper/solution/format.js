'use strict';
function pad2(n) {
  var s = String(n);
  while (s.length < 2) s = '0' + s;
  return s;
}
function formatMoney(cents) {
  return (cents < 0 ? '-' : '') + Math.floor(Math.abs(cents) / 100) + '.' + pad2(Math.abs(cents) % 100);
}
function formatTime(minutes) {
  return Math.floor(minutes / 60) + ':' + pad2(minutes % 60);
}
module.exports = { formatMoney: formatMoney, formatTime: formatTime };
