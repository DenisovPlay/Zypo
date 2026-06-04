const { app, dialog } = require('electron');
app.whenReady().then(() => {
  const result = dialog.showMessageBoxSync({
    message: "Test",
    buttons: ["OK", "Cancel"],
    checkboxLabel: "Check"
  });
  console.log("SYNC RESULT TYPE:", typeof result, result);
  
  dialog.showMessageBox({
    message: "Test Async",
    buttons: ["OK", "Cancel"],
    checkboxLabel: "Check"
  }).then(asyncResult => {
    console.log("ASYNC RESULT TYPE:", typeof asyncResult, asyncResult);
    app.quit();
  });
});
