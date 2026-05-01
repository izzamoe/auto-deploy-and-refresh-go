const http = require('http');

http.get('http://localhost:8080/admin/api/history', {
  headers: {
    'Authorization': 'Basic YWRtaW46cGFzc3dvcmQ=',
    'x-admin-request': 'true'
  }
}, (res) => {
  let data = '';
  res.on('data', chunk => data += chunk);
  res.on('end', () => console.log(data));
});
