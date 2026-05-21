// SaffronBridge Apps Script relay
// Deploy as Web App (Execute as: Me, Access: Anyone with the link)
//
// Set RELAY_URL to one of these patterns:
// 1) Direct to hybrid-exit port:
//    http://YOUR_VPS_IP:8099/relay
// 2) Via reverse proxy on 443:
//    https://your-domain/relay
//    (proxy forwards to 127.0.0.1:8099)
//
// The payload is forwarded unchanged. It can be either:
// - single upload JSON { filename, payload_b64 }
// - batch upload JSON { uploads: [{ filename, payload_b64 }, ...] }
//
// This relay returns JSON status to the client:
// {
//   ok: boolean,
//   relay_status: number,
//   relay_body: string,
//   error: string
// }

var RELAY_URL = 'http://YOUR_VPS_IP:8099/relay';
var SHARED_TOKEN = 'CHANGE_ME';

function doPost(e) {
  var out = {
    ok: false,
    relay_status: 0,
    relay_body: '',
    error: ''
  };

  try {
    if (!e || !e.postData || !e.postData.contents) {
      out.error = 'missing body';
      return ContentService.createTextOutput(JSON.stringify(out)).setMimeType(ContentService.MimeType.JSON);
    }

    var options = {
      method: 'post',
      contentType: 'application/json',
      payload: e.postData.contents,
      muteHttpExceptions: true,
      headers: {
        'Authorization': 'Bearer ' + SHARED_TOKEN
      }
    };

    var res = UrlFetchApp.fetch(RELAY_URL, options);
    out.relay_status = res.getResponseCode();
    out.relay_body = res.getContentText();
    out.ok = out.relay_status >= 200 && out.relay_status < 300;
  } catch (err) {
    out.error = String(err);
  }

  return ContentService.createTextOutput(JSON.stringify(out)).setMimeType(ContentService.MimeType.JSON);
}
