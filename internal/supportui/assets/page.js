(() => {
  for (const time of document.querySelectorAll('time[datetime]')) {
    const date = new Date(time.dateTime);
    if (!Number.isFinite(date.getTime())) continue;
    time.textContent = new Intl.DateTimeFormat(undefined, {
      dateStyle: 'medium', timeStyle: 'long',
    }).format(date);
  }
})();
