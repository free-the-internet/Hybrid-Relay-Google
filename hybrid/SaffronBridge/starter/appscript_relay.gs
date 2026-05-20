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

var RELAY_URL = 'http://YOUR_VPS_IP:8099/relay';
var SHARED_TOKEN = 'CHANGE_ME';

function doPost(e) {
  try {
    if (!e || !e.postData || !e.postData.contents) {
      return ContentService.createTextOutput('missing body').setMimeType(ContentService.MimeType.TEXT);
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
    return ContentService.createTextOutput(res.getContentText()).setMimeType(ContentService.MimeType.TEXT);
  } catch (err) {
    return ContentService.createTextOutput('relay error: ' + err).setMimeType(ContentService.MimeType.TEXT);
  }
}
