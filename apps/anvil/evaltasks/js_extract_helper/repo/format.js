'use strict';
function formatMoney(cents) {
  var s = String(Math.abs(cents) % 100);
  while (s.length < 2) s = '0' + s;
  return (cents < 0 ? '-' : '') + Math.floor(Math.abs(cents) / 100) + '.' + s;
}
function formatTime(minutes) {
  var s = String(minutes % 60);
  while (s.length < 2) s = '0' + s;
  return Math.floor(minutes / 60) + ':' + s;
}
module.exports = { formatMoney: formatMoney, formatTime: formatTime };
